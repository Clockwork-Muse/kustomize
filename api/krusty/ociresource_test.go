// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package krusty_test

import (
	"archive/tar"
	"bytes"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/crane"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	ociTypes "github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// pushOciArtifact pushes a map of filename→content as an OCI image to the given reference.
func pushOciArtifact(t *testing.T, ref string, files map[string]string) {
	t.Helper()

	// Pre-build the tar content to avoid streaming issues
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(content)),
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(content))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())

	tarContent := buf.Bytes()
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(tarContent)), nil
	}, tarball.WithMediaType(ociTypes.OCILayer))
	require.NoError(t, err)

	img, err := mutate.AppendLayers(empty.Image, layer)
	require.NoError(t, err)
	img = mutate.MediaType(img, ociTypes.OCIManifestSchema1)
	img = mutate.ConfigMediaType(img, ociTypes.MediaType("application/vnd.cncf.kustomize.layer.v1.tar+gzip"))
	require.NoError(t, err)
	err = crane.Push(img, ref, crane.Insecure, crane.WithPlatform(&v1.Platform{}))
	require.NoError(t, err)
}

func TestOciResourceInKustomization(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Push a base kustomization to the registry
	baseRef := address + "/myorg/base:v1.0.0"
	pushOciArtifact(t, baseRef, map[string]string{
		"kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- deployment.yaml
`,
		"deployment.yaml": `apiVersion: apps/v1
kind: Deployment
metadata:
  name: nginx
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:1.25
`,
	})

	// Create a local overlay that references the OCI base
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()

	ociRef := "oci://" + baseRef
	overlayKust := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ` + ociRef + `
namePrefix: prod-
namespace: production
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(overlayKust), 0644))

	// Run kustomize build
	opts := krusty.MakeDefaultOptions()
	m, err := krusty.MakeKustomizer(opts).Run(fSys, dir)
	require.NoError(t, err)

	// Verify output
	yaml, err := m.AsYaml()
	require.NoError(t, err)
	output := string(yaml)

	require.Contains(t, output, "name: prod-nginx")
	require.Contains(t, output, "namespace: production")
	require.Contains(t, output, "image: nginx:1.25")
}

func TestOciBuildDirectFromRegistry(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Push a kustomization directly
	ref := address + "/myorg/app:v2.0.0"
	pushOciArtifact(t, ref, map[string]string{
		"kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- service.yaml
namePrefix: svc-
`,
		"service.yaml": `apiVersion: v1
kind: Service
metadata:
  name: myapp
spec:
  ports:
  - port: 80
    targetPort: 8080
  selector:
    app: myapp
`,
	})

	// Build directly from the OCI reference
	fSys := filesys.MakeFsOnDisk()
	opts := krusty.MakeDefaultOptions()
	m, err := krusty.MakeKustomizer(opts).Run(fSys, "oci://"+ref)
	require.NoError(t, err)

	yaml, err := m.AsYaml()
	require.NoError(t, err)
	output := string(yaml)

	require.Contains(t, output, "name: svc-myapp")
	require.Contains(t, output, "port: 80")
}

func TestOciResourceWithSubdirectory(t *testing.T) {
	// Start in-process registry
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Push a multi-directory artifact
	ref := address + "/myorg/platform:v1.0.0"
	pushOciArtifact(t, ref, map[string]string{
		"base/kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- configmap.yaml
`,
		"base/configmap.yaml": `apiVersion: v1
kind: ConfigMap
metadata:
  name: app-config
data:
  key: value
`,
	})

	// Reference the subdirectory within the OCI artifact using //
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()

	ociRef := "oci://" + ref + "//base"
	overlayKust := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ` + ociRef + `
namePrefix: env-
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(overlayKust), 0644))

	opts := krusty.MakeDefaultOptions()
	m, err := krusty.MakeKustomizer(opts).Run(fSys, dir)
	require.NoError(t, err)

	yaml, err := m.AsYaml()
	require.NoError(t, err)
	output := string(yaml)

	require.Contains(t, output, "name: env-app-config")
	require.Contains(t, output, "key: value")
}

func TestOciResourceNotFoundReturnsError(t *testing.T) {
	// Start in-process registry (empty, nothing pushed)
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()

	ociRef := "oci://" + address + "/nonexistent/repo:v1.0.0"
	kust := `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- ` + ociRef + `
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte(kust), 0644))

	opts := krusty.MakeDefaultOptions()
	_, err := krusty.MakeKustomizer(opts).Run(fSys, dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "NAME_UNKNOWN")
}

func TestOciResourceWithInsecureReference(t *testing.T) {
	// Verify that oci:// references with name.Insecure option work
	// through the loader (the loader creates references without Insecure,
	// but localhost references should still work via HTTP fallback)
	server := httptest.NewServer(registry.New())
	t.Cleanup(server.Close)
	address := strings.TrimPrefix(server.URL, "http://")

	// Only localhost refs work without TLS in go-containerregistry
	if !strings.HasPrefix(address, "127.0.0.1") && !strings.HasPrefix(address, "localhost") {
		t.Skip("test requires localhost registry")
	}

	ref := address + "/myorg/simple:v1.0.0"
	pushOciArtifact(t, ref, map[string]string{
		"kustomization.yaml": `apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
- pod.yaml
`,
		"pod.yaml": `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: test
    image: busybox
`,
	})

	fSys := filesys.MakeFsOnDisk()
	opts := krusty.MakeDefaultOptions()
	m, err := krusty.MakeKustomizer(opts).Run(fSys, "oci://"+ref)
	require.NoError(t, err)

	yaml, err := m.AsYaml()
	require.NoError(t, err)
	require.Contains(t, string(yaml), "name: test-pod")
}
