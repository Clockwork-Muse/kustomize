// Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/blang/semver/v4"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Puller is a function that can pull an OCI artifact.
type Puller func(repoSpec *RepoSpec, fSys filesys.FileSystem, client *http.Client) error

// PullUsingOciManifest pulls an OCI artifact containing kustomization files.
// It handles both single manifests and multi-manifest indexes. For indexes,
// it looks for exactly one manifest with the kustomize artifact type.
func PullUsingOciManifest(repoSpec *RepoSpec, fSys filesys.FileSystem, client *http.Client) error {
	if repoSpec.Reference == nil {
		return fmt.Errorf("reference is required for pull")
	}

	// Create a temp directory for the pulled content if not already set
	if repoSpec.Dir == notPulled {
		dir, err := os.MkdirTemp("", "kustomize-oci-")
		if err != nil {
			return fmt.Errorf("creating temp directory for OCI pull: %w", err)
		}
		repoSpec.Dir = filesys.ConfirmedDir(dir)
	}

	ctx := context.Background()

	transport := http.DefaultTransport
	if client != nil && client.Transport != nil {
		transport = client.Transport
	}

	opts := []remote.Option{
		remote.WithTransport(transport),
		remote.WithAuthFromKeychain(Keychain()),
		remote.WithContext(ctx),
	}

	// Resolve semver constraint to a specific tag if needed
	if err := resolveSemverTag(repoSpec, opts); err != nil {
		return err
	}

	// Verify cosign signature if a key is configured
	if err := VerifyCosignSignature(repoSpec.Reference, ""); err != nil {
		return err
	}

	image, err := resolveImage(repoSpec, opts)
	if err != nil {
		return err
	}

	return extractImage(image, repoSpec, fSys)
}

// resolveSemverTag resolves a semver constraint to the highest matching tag
// from the registry by listing all available tags.
func resolveSemverTag(repoSpec *RepoSpec, opts []remote.Option) error {
	if repoSpec.SemverConstraint == "" {
		return nil
	}

	constraint, err := semver.ParseRange(repoSpec.SemverConstraint)
	if err != nil {
		return fmt.Errorf("invalid semver constraint %q: %w", repoSpec.SemverConstraint, err)
	}

	// List all tags from the repository
	repo := repoSpec.Reference.Context()
	tags, err := remote.List(repo, opts...)
	if err != nil {
		return fmt.Errorf("listing tags for semver resolution: %w", err)
	}

	// Find the highest version matching the constraint
	var best semver.Version
	var bestTag string
	found := false

	for _, tag := range tags {
		v, err := semver.ParseTolerant(tag)
		if err != nil {
			continue // skip non-semver tags
		}
		if constraint(v) {
			if !found || v.GT(best) {
				best = v
				bestTag = tag
				found = true
			}
		}
	}

	if !found {
		return fmt.Errorf("no tag matching semver constraint %q found in %s",
			repoSpec.SemverConstraint, repo.String())
	}

	// Rebuild the reference with the resolved tag
	resolved, err := name.NewTag(repo.String()+":"+bestTag,
		name.WithDefaultRegistry(""),
	)
	if err != nil {
		return fmt.Errorf("building resolved reference: %w", err)
	}
	repoSpec.Reference = resolved
	return nil
}

// resolveImage fetches the OCI reference and resolves it to a single v1.Image.
// If the reference points to an image index (multi-manifest), it searches for
// exactly one manifest matching the kustomize artifact type.
// Docker-format manifests are ignored.
func resolveImage(repoSpec *RepoSpec, opts []remote.Option) (v1.Image, error) {
	desc, err := remote.Get(repoSpec.Reference, opts...)
	if err != nil {
		return nil, err
	}

	switch desc.MediaType {
	case types.OCIManifestSchema1:
		return desc.Image()
	case types.OCIImageIndex, types.DockerManifestList:
		return resolveFromIndex(desc, opts)
	default:
		return nil, fmt.Errorf(
			"reference %q has no OCI kustomize artifact (media type %s)",
			repoSpec.Reference.String(), desc.MediaType)
	}
}

// resolveFromIndex searches an OCI image index for a kustomize artifact.
// It looks for manifests whose artifactType matches KustomizeArtifactType.
// If no artifacts match by type, it falls back to returning the first manifest
// (for compatibility with artifacts not published with kustomize).
// Returns an error if multiple kustomize artifacts are found.
func resolveFromIndex(desc *remote.Descriptor, opts []remote.Option) (v1.Image, error) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, fmt.Errorf("reading image index: %w", err)
	}

	indexManifest, err := idx.IndexManifest()
	if err != nil {
		return nil, fmt.Errorf("reading index manifest: %w", err)
	}

	if len(indexManifest.Manifests) == 0 {
		return nil, fmt.Errorf("image index contains no manifests")
	}

	// Search for manifests with the kustomize artifact type
	var matches []v1.Hash
	for _, m := range indexManifest.Manifests {
		if m.ArtifactType == KustomizeArtifactType {
			matches = append(matches, m.Digest)
		}
	}

	switch len(matches) {
	case 1:
		return idx.Image(matches[0])
	case 0:
		// No kustomize-typed artifacts found. Look for any OCI manifest
		// (reject Docker container images in the index).
		for _, m := range indexManifest.Manifests {
			if m.MediaType == types.OCIManifestSchema1 {
				return idx.Image(m.Digest)
			}
		}
		return nil, fmt.Errorf(
			"image index contains no OCI kustomize artifacts (found %d manifests, all Docker format or unrecognized)",
			len(indexManifest.Manifests))
	default:
		return nil, fmt.Errorf(
			"image index contains %d kustomize artifacts, expected exactly 1",
			len(matches))
	}
}

// extractImage extracts all files from an OCI image into the repoSpec directory.
func extractImage(image v1.Image, repoSpec *RepoSpec, fSys filesys.FileSystem) error {
	extracted := mutate.Extract(image)
	defer extracted.Close()

	reader := tar.NewReader(extracted)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		if header.FileInfo().IsDir() {
			continue
		}

		if !filepath.IsLocal(header.Name) {
			return fmt.Errorf("file reference '%s' not relative", header.Name)
		}

		destination := repoSpec.Dir.Join(header.Name)
		directory := filepath.Dir(destination)

		if err := fSys.MkdirAll(directory); err != nil {
			return err
		}

		if fp, err := fSys.Create(destination); err != nil {
			return err
		} else if _, err := io.Copy(fp, reader); err != nil {
			fp.Close()
			return err
		} else {
			fp.Close()
		}
	}

	return nil
}

// DoNothingPuller returns a puller that only sets
// pullDir field in the repoSpec.  It's assumed that
// the pullDir is associated with some fake filesystem
// used in a test.
func DoNothingPuller(dir filesys.ConfirmedDir) Puller {
	return func(rs *RepoSpec, fSys filesys.FileSystem, client *http.Client) error {
		rs.Dir = dir
		return nil
	}
}
