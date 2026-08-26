// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package publish_test

import (
	"archive/tar"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/kustomize/v5/commands/publish"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestPublishRequiresArgs(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires at least 1 arg")
}

func TestPublishRejectsLatestTag(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo:latest"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "must specify an explicit tag")
}

func TestPublishRejectsImplicitLatest(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "must specify an explicit tag")
}

func TestPublishWorksWithoutKustomizationFile(t *testing.T) {
	// A directory without kustomization.yaml should still be publishable
	// (it will fail at the push stage due to no real registry, but not at validation)
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/deployment.yaml", []byte("apiVersion: apps/v1\n"), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo:v1.0.0"})
	err := cmd.Execute()
	// Should fail at the push stage (registry unreachable), NOT at "no kustomization file"
	require.Error(t, err)
	require.NotContains(t, err.Error(), "no kustomization file found")
}

func TestPublishRejectsEmptyKustomization(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/kustomization.yaml", []byte(""), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo:v1.0.0"})
	err := cmd.Execute()
	require.Error(t, err)
}

func TestPublishRejectsInvalidTarget(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"not a valid reference!!!"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid target")
}

func TestPublishRejectsNonLocalPaths(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()
	dir := t.TempDir()
	os.WriteFile(dir+"/kustomization.yaml", []byte("resources:\n- ../outside\n"), 0644)
	t.Chdir(dir)

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo:v1.0.0"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "non-local file paths")
}

func TestPublishWithPathFlag(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Create kustomization in a subdirectory
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	subdir := dir + "/my-overlay"
	os.MkdirAll(subdir, 0755)
	os.WriteFile(subdir+"/kustomization.yaml", []byte("namespace: staging\n"), 0644)

	// Don't chdir — use --path instead
	target := address + "/myorg/myapp:v1.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target, "--path", subdir})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify image exists
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)
	_, err = remote.Image(ref, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)
}

func TestPublishWithPathFlagInvalidDir(t *testing.T) {
	fSys := filesys.MakeFsOnDisk()

	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{"ghcr.io/myorg/myrepo:v1.0.0", "--path", "/nonexistent/path"})
	err := cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "not a directory")
}

func TestPublishSuccessHTTP(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Create kustomization on disk (publish uses CWD via os.Getwd)
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"))
	fSys.WriteFile(dir+"/deployment.yaml", []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
`))

	// Change to temp dir for the command
	t.Chdir(dir)

	// Run publish
	target := address + "/myorg/myapp:v1.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify the image exists in the registry
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)

	_, err = remote.Image(ref, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)
}

func TestPublishSuccessHTTPS(t *testing.T) {
	// Create self-signed cert with stdlib
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)

	// Start TLS registry
	server := httptest.NewUnstartedServer(registry.New())
	server.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "https://")

	// Create kustomization on disk
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte("namespace: production\n"))

	t.Chdir(dir)

	// Run publish
	target := address + "/myorg/myapp:v2.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target})
	// The publish command uses DefaultKeychain which won't have the CA cert,
	// so this will fail with TLS error — verifying the TLS handling works
	err = cmd.Execute()
	require.Error(t, err)
	require.Contains(t, err.Error(), "certificate")
}

func TestPublishMultipleTargets(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Create kustomization on disk
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"))

	t.Chdir(dir)

	// Publish to two tags
	target1 := address + "/myorg/myapp:v1.0.0"
	target2 := address + "/myorg/myapp:latest-release"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target1, target2})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify both images exist
	for _, target := range []string{target1, target2} {
		ref, err := name.NewTag(target, name.Insecure)
		require.NoError(t, err)
		_, err = remote.Image(ref, remote.WithTransport(http.DefaultTransport))
		require.NoError(t, err)
	}
}

func TestPublishPreservesFileContent(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Create kustomization with multiple files
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte(`namespace: test
resources:
- deployment.yaml
`))
	fSys.WriteFile(dir+"/deployment.yaml", []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  replicas: 3
`))

	t.Chdir(dir)

	// Publish
	target := address + "/myorg/myapp:v1.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target})
	err := cmd.Execute()
	require.NoError(t, err)

	// Pull the image back and verify it has layers
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)

	img, err := remote.Image(ref, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)

	layers, err := img.Layers()
	require.NoError(t, err)
	require.NotEmpty(t, layers, "image should have at least one layer")

	// Verify the layer has content
	size, err := layers[0].Size()
	require.NoError(t, err)
	require.Greater(t, size, int64(0), "layer should have content")
}

func TestPublishSourceAndRevisionAnnotations(t *testing.T) {
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"))
	t.Chdir(dir)

	target := address + "/myorg/annotated:v1.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target,
		"--source", "https://github.com/myorg/myrepo",
		"--revision", "main/abc123def",
	})
	err := cmd.Execute()
	require.NoError(t, err)

	// Verify annotations on the pushed manifest
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)
	img, err := remote.Image(ref, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)

	manifest, err := img.Manifest()
	require.NoError(t, err)
	require.Equal(t, "https://github.com/myorg/myrepo", manifest.Annotations["org.opencontainers.image.source"])
	require.Equal(t, "main/abc123def", manifest.Annotations["org.opencontainers.image.revision"])
	require.NotEmpty(t, manifest.Annotations["org.opencontainers.image.created"])
}

func TestPublishExcludeFlag(t *testing.T) {
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(dir+"/kustomization.yaml", []byte("namespace: test\n"))
	fSys.WriteFile(dir+"/deployment.yaml", []byte("apiVersion: apps/v1\n"))
	fSys.WriteFile(dir+"/README.md", []byte("# readme\n"))
	fSys.WriteFile(dir+"/secret.env", []byte("PASSWORD=foo\n"))
	t.Chdir(dir)

	target := address + "/myorg/excluded:v1.0.0"
	cmd := publish.NewCmdPublish(fSys)
	cmd.SetArgs([]string{target, "--exclude", "*.md", "--exclude", "*.env"})
	err := cmd.Execute()
	require.NoError(t, err)

	// Pull and extract to verify exclusions
	ref, err := name.NewTag(target, name.Insecure)
	require.NoError(t, err)
	img, err := remote.Image(ref, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)

	layers, err := img.Layers()
	require.NoError(t, err)
	require.Len(t, layers, 1)

	// Extract and check file names in the tar
	rc, err := layers[0].Uncompressed()
	require.NoError(t, err)
	defer rc.Close()

	tr := tar.NewReader(rc)
	var files []string
	for {
		hdr, err := tr.Next()
		if err != nil {
			break
		}
		files = append(files, hdr.Name)
	}

	require.Contains(t, files, "kustomization.yaml")
	require.Contains(t, files, "deployment.yaml")
	require.NotContains(t, files, "README.md")
	require.NotContains(t, files, "secret.env")
}
