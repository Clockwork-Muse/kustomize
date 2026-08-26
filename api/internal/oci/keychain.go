// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
)

const (
	// EnvOCIUsername is the environment variable for OCI registry username.
	EnvOCIUsername = "KUSTOMIZE_OCI_USERNAME"
	// EnvOCIPassword is the environment variable for OCI registry password/token.
	EnvOCIPassword = "KUSTOMIZE_OCI_PASSWORD"
)

// envKeychain is a custom authn.Keychain that checks environment variables
// before falling back to the default Docker keychain.
type envKeychain struct{}

// Resolve implements authn.Keychain. If KUSTOMIZE_OCI_USERNAME and
// KUSTOMIZE_OCI_PASSWORD environment variables are set, they are used
// for authentication. Otherwise, it delegates to authn.DefaultKeychain
// which reads from ~/.docker/config.json.
func (k *envKeychain) Resolve(res authn.Resource) (authn.Authenticator, error) {
	username := os.Getenv(EnvOCIUsername)
	password := os.Getenv(EnvOCIPassword)

	if username != "" && password != "" {
		return authn.FromConfig(authn.AuthConfig{
			Username: username,
			Password: password,
		}), nil
	}

	return authn.DefaultKeychain.Resolve(res)
}

// Keychain returns the OCI keychain used for registry authentication.
// It checks KUSTOMIZE_OCI_USERNAME/KUSTOMIZE_OCI_PASSWORD env vars first,
// then falls back to the Docker config (~/.docker/config.json).
func Keychain() authn.Keychain {
	return &envKeychain{}
}
