// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"time"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	ociTypes "github.com/google/go-containerregistry/pkg/v1/types"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/errors"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// PushOptions configures the push operation.
type PushOptions struct {
	fSys            filesys.FileSystem
	root            filesys.ConfirmedDir
	kustomization   *types.Kustomization
	targets         []name.Tag
	annotations     map[string]string
	excludePatterns []string
	http            *http.Client
}

// SetFileSystem sets the filesystem for the push operation.
func (o *PushOptions) SetFileSystem(fSys filesys.FileSystem) {
	o.fSys = fSys
}

// SetRoot sets the root directory to publish.
func (o *PushOptions) SetRoot(root filesys.ConfirmedDir) {
	o.root = root
}

// SetKustomization sets the kustomization to validate before pushing.
func (o *PushOptions) SetKustomization(k *types.Kustomization) {
	o.kustomization = k
}

// SetTargets sets the OCI registry targets to push to.
func (o *PushOptions) SetTargets(targets []name.Tag) {
	o.targets = targets
}

// SetAnnotations sets OCI manifest annotations for the push.
func (o *PushOptions) SetAnnotations(annotations map[string]string) {
	o.annotations = annotations
}

// SetExcludePatterns sets file exclusion patterns (gitignore format) for the push.
func (o *PushOptions) SetExcludePatterns(patterns []string) {
	o.excludePatterns = patterns
}

// SetHTTPClient sets a custom HTTP client (e.g. for custom TLS).
func (o *PushOptions) SetHTTPClient(client *http.Client) {
	o.http = client
}

func validatePath(path string, elementType string) error {
	if path == "" {
		return nil
	}

	u, err := url.Parse(path)
	if err != nil {
		return err
	}
	if u.Scheme == "file" && u.Host == "" {
		return errors.Errorf("Path %s in element %s is a file url relative to localhost", path, elementType)
	} else if u.IsAbs() || u.Host != "" {
		// Other schemes or host-rooted URLs are assumed to be valid....
		return nil
	}

	path = u.Path

	if !filepath.IsLocal(path) {
		return errors.Errorf("Path '%s' in element %s is not local", path, elementType)
	}

	return nil
}

func iteratePathElementsSimple(elements []string, elementType string, errors []error) []error {
	return iteratePathElements(elements, func(x string) string { return x }, elementType, errors)
}

func iteratePathElements[T any](elements []T, fn func(T) string, elementType string, errors []error) []error {
	for _, element := range elements {
		path := fn(element)

		if err := validatePath(path, elementType); err != nil {
			errors = append(errors, err)
		}
	}

	return errors
}

func validateFilePaths(k *types.Kustomization) *[]error {
	errors := []error{}

	errors = iteratePathElementsSimple(k.Components, "Components", errors)
	errors = iteratePathElements(k.Patches, func(x types.Patch) string { return x.Path }, "Patches", errors)
	errors = iteratePathElements(k.Replacements, func(x types.ReplacementField) string { return x.Path }, "Replacements", errors)
	errors = iteratePathElementsSimple(k.Resources, "Resources", errors)
	errors = iteratePathElementsSimple(k.Crds, "Crds", errors)

	for _, generator := range k.ConfigMapGenerator {
		errors = iteratePathElementsSimple(generator.EnvSources, "ConfigMapGenerator", errors)
		errors = iteratePathElementsSimple(generator.FileSources, "ConfigMapGenerator", errors)
	}
	for _, generator := range k.SecretGenerator {
		errors = iteratePathElementsSimple(generator.EnvSources, "SecretGenerator", errors)
		errors = iteratePathElementsSimple(generator.FileSources, "SecretGenerator", errors)
	}

	for _, charts := range k.HelmCharts {
		errors = iteratePathElementsSimple(append(charts.AdditionalValuesFiles, charts.ValuesFile), "HelmCharts", errors)
	}

	errors = iteratePathElementsSimple(k.Configurations, "Configurations", errors)
	errors = iteratePathElementsSimple(k.Generators, "Generators", errors)
	errors = iteratePathElementsSimple(k.Transformers, "Transformers", errors)
	errors = iteratePathElementsSimple(k.Validators, "Validators", errors)

	return &errors
}

// PushToOciRegistries pushes a kustomization directory to one or more OCI registries.
func PushToOciRegistries(options *PushOptions) error {
	if len(options.targets) == 0 {
		return errors.Errorf("At least one target is required.")
	}

	// If a kustomization was provided, validate it before pushing.
	// This is optional — a directory can be published without a kustomization file.
	if options.kustomization != nil {
		if err := options.kustomization.CheckEmpty(); err != nil {
			return err
		}

		if errs := options.kustomization.EnforceFields(); len(errs) > 0 {
			return errors.Errorf("kustomization has field errors: %v", errs)
		}

		if deprecated := options.kustomization.CheckDeprecatedFields(); deprecated != nil && len(*deprecated) > 0 {
			for _, field := range *deprecated {
				log.Println(field)
			}
		}

		options.kustomization.FixKustomization()

		// Validate that paths are either remote URLs or local to the kustomization root.
		if pathErrors := validateFilePaths(options.kustomization); pathErrors != nil && len(*pathErrors) > 0 {
			return errors.Errorf("kustomization includes non-local file paths: %v", pathErrors)
		}
	}

	// Build OCI image from the directory contents
	dir := options.root.String()
	if dir == "" {
		// Default to current working directory
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		dir = cwd
	}
	img, err := buildImageFromDirectory(options.fSys, dir, options.excludePatterns)
	if err != nil {
		return fmt.Errorf("building image from directory: %w", err)
	}

	// Apply OCI annotations to the manifest
	anns := make(map[string]string)
	anns["org.opencontainers.image.created"] = time.Now().UTC().Format(time.RFC3339)
	for k, v := range options.annotations {
		anns[k] = v
	}
	img = mutate.Annotations(img, anns).(v1.Image)

	// Configure transport
	transport := http.DefaultTransport
	if options.http != nil && options.http.Transport != nil {
		transport = options.http.Transport
	}

	// Push to each target
	for _, tag := range options.targets {
		err := remote.Write(tag, img,
			remote.WithTransport(transport),
			remote.WithAuthFromKeychain(Keychain()),
		)
		if err != nil {
			return err
		}
	}

	return nil
}

// KustomizeArtifactType is the OCI artifact type for kustomization bundles.
const KustomizeArtifactType = "application/vnd.cncf.kustomize.layer.v1.tar+gzip"

// buildImageFromDirectory creates an OCI artifact with a single tar layer containing
// all files from the given directory. The artifact uses OCI media types with a
// kustomize-specific config media type to identify it as a kustomization bundle.
func buildImageFromDirectory(fSys filesys.FileSystem, dir string, excludePatterns []string) (v1.Image, error) {
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return tarDirectory(fSys, dir, excludePatterns)
	}, tarball.WithMediaType(ociTypes.OCILayer))
	if err != nil {
		return nil, err
	}

	img, err := mutate.AppendLayers(empty.Image, layer)
	if err != nil {
		return nil, err
	}

	// Set OCI manifest media type
	img = mutate.MediaType(img, ociTypes.OCIManifestSchema1)
	// Set config media type to kustomize artifact type.
	// When config is not a standard image config type, it becomes the artifactType
	// in the OCI manifest, making this artifact distinguishable from container images.
	img = mutate.ConfigMediaType(img, ociTypes.MediaType(KustomizeArtifactType))

	return img, nil
}

// tarDirectory creates a tar archive of all files in the given directory.
// Files matching patterns in .kustomizeignore or excludePatterns are skipped.
func tarDirectory(fSys filesys.FileSystem, dir string, excludePatterns []string) (io.ReadCloser, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	// Load .kustomizeignore patterns
	matcher := buildIgnoreMatcher(fSys, dir, excludePatterns)

	err := fSys.Walk(dir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Check if file should be excluded
		if matcher != nil && matcher(relPath) {
			return nil
		}

		content, err := fSys.ReadFile(path)
		if err != nil {
			return err
		}

		hdr := &tar.Header{
			Name: relPath,
			Mode: 0644,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(content); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}

	return io.NopCloser(&buf), nil
}


// buildIgnoreMatcher creates a function that returns true for paths that should be excluded.
// It reads .kustomizeignore from the directory (gitignore-style patterns) and merges with
// additional exclude patterns.
func buildIgnoreMatcher(fSys filesys.FileSystem, dir string, extraPatterns []string) func(string) bool {
	var patterns []string

	// Load .kustomizeignore if present
	ignoreFile := filepath.Join(dir, ".kustomizeignore")
	if content, err := fSys.ReadFile(ignoreFile); err == nil {
		for _, line := range filepath.SplitList(string(content)) {
			// SplitList isn't right for newlines, do it manually
			_ = line
		}
		// Parse line by line
		for _, line := range splitLines(string(content)) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			patterns = append(patterns, line)
		}
	}

	// Add extra patterns from --exclude flag
	patterns = append(patterns, extraPatterns...)

	if len(patterns) == 0 {
		return nil
	}

	return func(relPath string) bool {
		// Never exclude kustomization files
		base := filepath.Base(relPath)
		for _, kf := range []string{"kustomization.yaml", "kustomization.yml", "Kustomization"} {
			if base == kf {
				return false
			}
		}

		// Always exclude .kustomizeignore itself
		if base == ".kustomizeignore" {
			return true
		}

		for _, pattern := range patterns {
			// Try matching against the full relative path and just the filename
			if matched, _ := filepath.Match(pattern, relPath); matched {
				return true
			}
			if matched, _ := filepath.Match(pattern, base); matched {
				return true
			}
		}
		return false
	}
}

// splitLines splits text into lines, handling both \n and \r\n.
func splitLines(s string) []string {
	return strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
}
