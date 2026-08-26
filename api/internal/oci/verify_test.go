// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"os"
	"os/exec"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/require"

	loctest "sigs.k8s.io/kustomize/api/testutils/localizertest"
)

func TestVerifyCosignNoKeyConfigured(t *testing.T) {
	// When no key is set, verification is a no-op (passes)
	t.Setenv(EnvCosignKey, "")

	ref, _ := name.ParseReference("ghcr.io/test/repo:v1.0.0")
	err := VerifyCosignSignature(ref, "")
	require.NoError(t, err)
}

func TestVerifyCosignKeySetButCosignNotInstalled(t *testing.T) {
	// When a key is set but cosign isn't on PATH, return clear error
	t.Setenv(EnvCosignKey, "/path/to/cosign.pub")
	t.Setenv("PATH", t.TempDir()) // empty PATH so cosign can't be found

	ref, _ := name.ParseReference("ghcr.io/test/repo:v1.0.0")
	err := VerifyCosignSignature(ref, "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cosign is not installed")
}

func TestVerifyCosignExplicitKeyPath(t *testing.T) {
	// Explicit keyPath takes priority over env var
	t.Setenv(EnvCosignKey, "")
	t.Setenv("PATH", t.TempDir()) // no cosign

	ref, _ := name.ParseReference("ghcr.io/test/repo:v1.0.0")
	err := VerifyCosignSignature(ref, "/explicit/key.pub")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cosign is not installed")
}

func TestVerifyCosignIntegration(t *testing.T) {
	// Skip if cosign isn't installed
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign not installed, skipping integration test")
	}

	// Enable insecure mode for HTTP test registry
	t.Setenv(EnvCosignInsecure, "1")

	// Start in-process registry
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	// Push an artifact
	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}
	src, _, target := loctest.PrepareFs(t, nil, kustomization)
	ref := toReference(t, address+"/myorg/signed:v1.0.0", name.Insecure)
	pushArtifact(t, src, target.String(), ref, "", "", nil)

	// Generate a cosign key pair
	keyDir := t.TempDir()
	keyFile := keyDir + "/cosign.key"
	pubFile := keyDir + "/cosign.pub"
	t.Setenv("COSIGN_PASSWORD", "")

	genCmd := exec.Command("cosign", "generate-key-pair",
		"--output-key-prefix", keyDir+"/cosign")
	genCmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err := genCmd.CombinedOutput()
	require.NoError(t, err, "cosign generate-key-pair failed: %s", string(out))
	require.FileExists(t, keyFile)
	require.FileExists(t, pubFile)

	// Create signing config with no tlog (avoids deprecated --tlog-upload flag)
	signingConfig := createNoTlogSigningConfig(t)

	// Sign the image
	signCmd := exec.Command("cosign", "sign",
		"--key", keyFile,
		"--allow-insecure-registry",
		"--allow-http-registry",
		"--signing-config", signingConfig,
		ref.String())
	signCmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err = signCmd.CombinedOutput()
	require.NoError(t, err, "cosign sign failed: %s", string(out))

	// Verify with the correct key — should pass
	err = VerifyCosignSignature(ref, pubFile)
	require.NoError(t, err)
}

func TestVerifyCosignFailsWithWrongKey(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign not installed, skipping integration test")
	}

	address, _ := createRegistry(t, "", "", false)
	t.Setenv(EnvCosignInsecure, "1")
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}
	src, _, target := loctest.PrepareFs(t, nil, kustomization)
	ref := toReference(t, address+"/myorg/wrongkey:v1.0.0", name.Insecure)
	pushArtifact(t, src, target.String(), ref, "", "", nil)

	// Generate key pair A and sign with it
	keyDirA := t.TempDir()
	t.Setenv("COSIGN_PASSWORD", "")

	genCmd := exec.Command("cosign", "generate-key-pair",
		"--output-key-prefix", keyDirA+"/cosign")
	genCmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err := genCmd.CombinedOutput()
	require.NoError(t, err, "generate key A: %s", string(out))

	signingConfig := createNoTlogSigningConfig(t)

	signCmd := exec.Command("cosign", "sign",
		"--key", keyDirA+"/cosign.key",
		"--allow-insecure-registry",
		"--allow-http-registry",
		"--signing-config", signingConfig,
		ref.String())
	signCmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err = signCmd.CombinedOutput()
	require.NoError(t, err, "cosign sign: %s", string(out))

	// Generate a DIFFERENT key pair B
	keyDirB := t.TempDir()
	genCmd2 := exec.Command("cosign", "generate-key-pair",
		"--output-key-prefix", keyDirB+"/cosign")
	genCmd2.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err = genCmd2.CombinedOutput()
	require.NoError(t, err, "generate key B: %s", string(out))

	// Verify with key B — should FAIL (signed with key A)
	err = VerifyCosignSignature(ref, keyDirB+"/cosign.pub")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cosign verification failed")
}

func TestVerifyCosignFailsUnsignedImage(t *testing.T) {
	if _, err := exec.LookPath("cosign"); err != nil {
		t.Skip("cosign not installed, skipping integration test")
	}

	address, _ := createRegistry(t, "", "", false)
	t.Setenv(EnvCosignInsecure, "1")
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}
	src, _, target := loctest.PrepareFs(t, nil, kustomization)
	ref := toReference(t, address+"/myorg/unsigned:v1.0.0", name.Insecure)
	pushArtifact(t, src, target.String(), ref, "", "", nil)

	// Generate a key but DON'T sign the image
	keyDir := t.TempDir()
	t.Setenv("COSIGN_PASSWORD", "")
	genCmd := exec.Command("cosign", "generate-key-pair",
		"--output-key-prefix", keyDir+"/cosign")
	genCmd.Env = append(os.Environ(), "COSIGN_PASSWORD=")
	out, err := genCmd.CombinedOutput()
	require.NoError(t, err, "generate key: %s", string(out))

	// Verify unsigned image — should fail
	err = VerifyCosignSignature(ref, keyDir+"/cosign.pub")
	require.Error(t, err)
	require.Contains(t, err.Error(), "cosign verification failed")
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// createNoTlogSigningConfig writes a signing config JSON that disables transparency log upload.
// This is required for cosign v3+ which deprecated --tlog-upload=false.
func createNoTlogSigningConfig(t *testing.T) string {
	t.Helper()
	config := `{"mediaType":"application/vnd.dev.sigstore.signingconfig.v0.2+json","rekorTlogConfig":{},"tsaConfig":{}}`
	path := t.TempDir() + "/signing-config.json"
	require.NoError(t, os.WriteFile(path, []byte(config), 0644))
	return path
}
