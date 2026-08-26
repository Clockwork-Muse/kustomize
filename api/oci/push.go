// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

// Package oci provides public APIs for OCI registry operations.
package oci

import (
	"net/http"

	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/kustomize/api/internal/oci"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// PushOptions configures a push to OCI registries.
type PushOptions struct {
	internal oci.PushOptions
}

// SetFileSystem sets the filesystem for the push operation.
func (o *PushOptions) SetFileSystem(fSys filesys.FileSystem) {
	o.internal.SetFileSystem(fSys)
}

// SetRoot sets the root directory to publish.
func (o *PushOptions) SetRoot(root filesys.ConfirmedDir) {
	o.internal.SetRoot(root)
}

// SetKustomization sets the kustomization to validate before pushing.
func (o *PushOptions) SetKustomization(k *types.Kustomization) {
	o.internal.SetKustomization(k)
}

// SetTargets sets the OCI registry targets to push to.
func (o *PushOptions) SetTargets(targets []name.Tag) {
	o.internal.SetTargets(targets)
}

// SetHTTPClient sets a custom HTTP client (e.g. for custom TLS).
func (o *PushOptions) SetHTTPClient(client *http.Client) {
	o.internal.SetHTTPClient(client)
}

// SetAnnotations sets OCI manifest annotations (e.g. source URL, revision).
func (o *PushOptions) SetAnnotations(annotations map[string]string) {
	o.internal.SetAnnotations(annotations)
}

// SetExcludePatterns sets file exclusion patterns (gitignore format).
func (o *PushOptions) SetExcludePatterns(patterns []string) {
	o.internal.SetExcludePatterns(patterns)
}

// Push packages a kustomization directory and pushes it to one or more OCI registries.
func Push(opts *PushOptions) error {
	return oci.PushToOciRegistries(&opts.internal)
}
