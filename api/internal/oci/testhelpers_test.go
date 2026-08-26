// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	ociTypes "github.com/google/go-containerregistry/pkg/v1/types"
	"sigs.k8s.io/kustomize/api/filesys"
)

// testCert holds a self-signed certificate and key for testing.
type testCert struct {
	Cert    *x509.Certificate
	CertPEM []byte
	KeyPEM  []byte
}

// newSelfSignedCert generates a self-signed TLS certificate for testing.
func newSelfSignedCert(t *testing.T, hosts ...string) *testCert {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{Organization: []string{"Test"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:         true,
		BasicConstraintsValid: true,
	}

	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, h)
		}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatal(err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	return &testCert{Cert: cert, CertPEM: certPEM, KeyPEM: keyPEM}
}

// toReference parses an OCI reference string, fatally failing the test on error.
func toReference(t *testing.T, reference string, opts ...name.Option) name.Reference {
	t.Helper()

	if ref, err := name.ParseReference(reference, opts...); err != nil {
		t.Fatal(err)
		return nil
	} else {
		return ref
	}
}

// toClient creates an http.Client that trusts the given CA certificate.
// Returns http.DefaultClient if cert is nil.
func toClient(cert *testCert) *http.Client {
	if cert == nil {
		return http.DefaultClient
	}

	pool := x509.NewCertPool()
	pool.AddCert(cert.Cert)

	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		},
	}
}

// createRegistry starts an in-process OCI registry using httptest.Server.
// It supports optional TLS and optional basic auth.
// Returns the host:port address and the CA certificate (if TLS is enabled).
func createRegistry(t *testing.T, username string, password string, useTLS bool) (address string, cert *testCert) {
	t.Helper()

	regHandler := registry.New()

	var handler http.Handler
	if username != "" && password != "" {
		handler = basicAuthMiddleware(username, password, regHandler)
	} else {
		handler = regHandler
	}

	var server *httptest.Server
	if useTLS {
		caCert := newSelfSignedCert(t, "localhost", "127.0.0.1")

		tlsCert, err := tls.X509KeyPair(caCert.CertPEM, caCert.KeyPEM)
		if err != nil {
			t.Fatal(err)
		}

		server = httptest.NewUnstartedServer(handler)
		server.TLS = &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
		}
		server.StartTLS()
		t.Cleanup(server.Close)

		address = strings.TrimPrefix(server.URL, "https://")
		return address, caCert
	}

	server = httptest.NewServer(handler)
	t.Cleanup(server.Close)

	address = strings.TrimPrefix(server.URL, "http://")
	return address, nil
}

// basicAuthMiddleware wraps an http.Handler with basic auth enforcement.
func basicAuthMiddleware(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != username || p != password {
			w.Header().Set("WWW-Authenticate", `Basic realm="Registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprintf(w, `{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// createDockerConfig writes a Docker auth config file to a temp dir and sets DOCKER_CONFIG.
func createDockerConfig(t *testing.T, address string, user string, password string) {
	t.Helper()

	authDirectory := t.TempDir()
	authFile, err := os.Create(filepath.Join(authDirectory, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer authFile.Close()

	config := map[string]interface{}{
		"auths": map[string]interface{}{
			address: map[string]string{
				"username": user,
				"password": password,
			},
		},
	}

	encoder := json.NewEncoder(authFile)
	if err := encoder.Encode(config); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DOCKER_CONFIG", authDirectory)
}

// pushArtifact pushes files from a filesystem to an in-process registry using go-containerregistry.
func pushArtifact(t *testing.T, fSys filesys.FileSystem, folder string, ref name.Reference, username string, password string, cert *testCert) {
	t.Helper()

	var files []fileEntry
	fSys.Walk(folder, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		b, err := fSys.ReadFile(path)
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(folder, path)
		if err != nil {
			return err
		}

		files = append(files, fileEntry{name: relPath, content: b})
		return nil
	})

	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return newTarBuffer(files)
	}, tarball.WithMediaType(ociTypes.OCILayer))
	if err != nil {
		t.Fatal(err)
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		t.Fatal(err)
	}
	// Use OCI media types to match what the puller expects
	img = mutate.MediaType(img, ociTypes.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, ociTypes.MediaType(KustomizeArtifactType))

	opts := []crane.Option{
		crane.WithPlatform(&v1.Platform{}),
	}

	if cert != nil {
		pool := x509.NewCertPool()
		pool.AddCert(cert.Cert)
		transport := &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs: pool,
			},
		}
		opts = append(opts, crane.WithTransport(transport))
	} else {
		opts = append(opts, crane.Insecure)
	}

	if username != "" {
		opts = append(opts, crane.WithAuth(&authn.Basic{
			Username: username,
			Password: password,
		}))
	}

	if err := crane.Push(img, ref.Name(), opts...); err != nil {
		t.Fatal(err)
	}
}

type fileEntry struct {
	name    string
	content []byte
}
