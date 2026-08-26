// Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"sigs.k8s.io/kustomize/kyaml/errors"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// Used as a temporary non-empty occupant of the pullDir
// field, as something distinguishable from the empty string
// in various outputs (especially tests). Not using an
// actual directory name here, as that's a temporary directory
// with a unique name that isn't created until pull time.
const notPulled = filesys.ConfirmedDir("/notPulled")

// RepoSpec specifies an OCI repository and a tag
type RepoSpec struct {
	// Raw, original spec, used to look for cycles.
	raw string

	// Dir is where the manifest is pulled to.
	Dir filesys.ConfirmedDir

	// If a remote artifact, the reference to the remote artifact
	Reference name.Reference

	// Relative path in the repository, and in the pullDir,
	// to a Kustomization.
	KustRootPath string

	// SemverConstraint is set when the tag is a semver range (e.g., ">=1.0.0").
	// When set, the puller resolves the highest matching tag from the registry.
	SemverConstraint string
}

// PullSpec returns the OCI reference string suitable for pulling.
func (x *RepoSpec) PullSpec() string {
	return x.Reference.String()
}

func (x *RepoSpec) PullDir() filesys.ConfirmedDir {
	return x.Dir
}

func (x *RepoSpec) Raw() string {
	return x.raw
}

func (x *RepoSpec) AbsPath() string {
	return x.Dir.Join(x.KustRootPath)
}

func (x *RepoSpec) Cleaner(fSys filesys.FileSystem) func() error {
	return func() error { return fSys.RemoveAll(x.Dir.String()) }
}

const (
	tagSeparator    = ":"
	digestSeparator = "@"
	pathSeparator   = "/" // do not use filepath.Separator, as this is a URL
	rootSeparator   = "//"
)

// NewRepoSpecFromURL parses OCI reference paths.
// From strings like oci://ghcr.io/someOrg/someRepository:someTag or
// oci://ghcr.io/someOrg/someRepository:sha256:<digest>, extract
// the different parts of URL, set into a RepoSpec object and return RepoSpec object.
// It MUST return an error if the input is not an oci-like URL, as this is used by some code paths
// to distinguish between local and remote paths.
//
// In particular, NewManifestSpecFromURL separates the URL used to pull the manifest from the
// elements Kustomize uses for other purposes (e.g. query params that turn into args, and
// the path to the kustomization root within the repo).
func NewRepoSpecFromURL(n string) (*RepoSpec, error) {
	repoSpec := &RepoSpec{raw: n, Dir: notPulled}

	n, err := trimScheme(n)
	if err != nil {
		return nil, err
	}

	repoSpec.KustRootPath, n, err = extractRoot(n)
	if err != nil {
		return nil, err
	}

	// Check if the tag portion is a semver constraint (contains range chars)
	if constraint, repo, ok := extractSemverConstraint(n); ok {
		repoSpec.SemverConstraint = constraint
		// Parse reference without the constraint tag (use repo as-is)
		repoSpec.Reference, err = name.ParseReference(repo,
			name.WithDefaultRegistry(""),
			name.WithDefaultTag("latest"),
		)
	} else {
		repoSpec.Reference, err = name.ParseReference(n,
			name.WithDefaultRegistry(""),
			name.WithDefaultTag("latest"),
		)
	}
	if err != nil {
		return nil, err
	}
	if repoSpec.Reference.Context().Registry.Name() == "" || repoSpec.Reference.Context().RepositoryStr() == "" {
		return nil, fmt.Errorf("invalid reference: missing registry or repository")
	}

	return repoSpec, nil
}

func extractRoot(n string) (kustRoot string, rest string, err error) {
	if rootIndex := strings.LastIndex(n, rootSeparator); rootIndex >= 0 {
		kustRoot = n[rootIndex+len(rootSeparator):]

		if kustRoot == "" {
			return "", "", errors.Errorf("failed to parse root path segment")
		}
		if kustRootPathExitsRepo(kustRoot) {
			return "", "", errors.Errorf("root path exits repo")
		}

		return kustRoot, n[:rootIndex], nil
	}

	return "", n, nil
}

func kustRootPathExitsRepo(kustRootPath string) bool {
	cleanedPath := filepath.Clean(strings.TrimPrefix(kustRootPath, string(filepath.Separator)))
	pathElements := strings.Split(cleanedPath, string(filepath.Separator))
	return len(pathElements) > 0 &&
		pathElements[0] == filesys.ParentDir
}

const ociScheme = "oci://"

// semverRangeChars are characters that indicate a tag is a semver constraint rather than a literal tag.
const semverRangeChars = "><=~^|*x "

// extractSemverConstraint checks if the reference string has a tag portion that looks like a
// semver constraint (e.g., ">=1.0.0 <2.0.0"). If so, it returns the constraint and the
// repository portion without the tag. Returns ok=false if no constraint detected.
func extractSemverConstraint(ref string) (constraint string, repo string, ok bool) {
	// Find the last colon that separates repo from tag
	// But not if it's part of a port (host:port/repo) — those come before the first /
	lastColon := strings.LastIndex(ref, ":")
	if lastColon < 0 {
		return "", "", false
	}

	// Make sure the colon is after the first slash (so it's a tag, not a port)
	firstSlash := strings.Index(ref, "/")
	if firstSlash >= 0 && lastColon < firstSlash {
		return "", "", false
	}

	tag := ref[lastColon+1:]
	if !isSemverConstraint(tag) {
		return "", "", false
	}

	return tag, ref[:lastColon], true
}

// isSemverConstraint returns true if the tag contains characters indicating
// it's a semver range/constraint rather than a literal version tag.
func isSemverConstraint(tag string) bool {
	return strings.ContainsAny(tag, semverRangeChars)
}

func trimScheme(s string) (rest string, err error) {
	if len(ociScheme) <= len(s) && strings.ToLower(s[:len(ociScheme)]) == ociScheme {
		return s[len(ociScheme):], nil
	}
	return "", fmt.Errorf("unsupported scheme")
}
