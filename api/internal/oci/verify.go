// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/google/go-containerregistry/pkg/name"
)

const (
	// EnvCosignKey is the environment variable for the cosign public key path.
	EnvCosignKey = "KUSTOMIZE_OCI_COSIGN_KEY"

	// EnvCosignInsecure allows insecure registries and skips tlog verification.
	// Set to "1" or "true" for testing with HTTP registries.
	EnvCosignInsecure = "KUSTOMIZE_OCI_COSIGN_INSECURE"
)

// VerifyCosignSignature verifies the OCI artifact signature using the cosign CLI.
// It requires cosign to be installed and available on PATH.
// If keyPath is empty, it checks the KUSTOMIZE_OCI_COSIGN_KEY env var.
// Returns nil if verification succeeds or if no key is configured (verification disabled).
func VerifyCosignSignature(ref name.Reference, keyPath string) error {
	if keyPath == "" {
		keyPath = os.Getenv(EnvCosignKey)
	}
	if keyPath == "" {
		// No key configured — verification not requested
		return nil
	}

	cosignPath, err := exec.LookPath("cosign")
	if err != nil {
		return fmt.Errorf("cosign verification requested (key=%s) but cosign is not installed: %w", keyPath, err)
	}

	args := []string{"verify", "--key", keyPath}

	// Allow insecure registries for testing
	if isInsecure() {
		args = append(args, "--allow-insecure-registry", "--allow-http-registry", "--insecure-ignore-tlog")
	}

	args = append(args, ref.String())

	cmd := exec.Command(cosignPath, args...)
	cmd.Env = os.Environ()

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign verification failed for %s: %s\n%s", ref.String(), err, string(output))
	}

	return nil
}

// isInsecure returns true if KUSTOMIZE_OCI_COSIGN_INSECURE is set.
func isInsecure() bool {
	v := os.Getenv(EnvCosignInsecure)
	return v == "1" || v == "true"
}
