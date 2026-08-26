// Copyright 2025 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package publish

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/cobra"
	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/oci"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

// NewCmdPublish returns a new publish command.
func NewCmdPublish(fSys filesys.FileSystem) *cobra.Command {
	var path string
	var source string
	var revision string
	var exclude []string

	cmd := &cobra.Command{
		Use:   "publish <registry/repository:tag> [registry/repository:tag...]",
		Short: "[Alpha] Publishes a kustomization directory to an OCI registry",
		Long: `[Alpha] Packages the kustomization directory and pushes it as an OCI
artifact to one or more registry targets.

By default, the current working directory is published. Use --path to
specify a different kustomization directory.

Targets must include an explicit tag (e.g. :v1.0.0). The artifact can then
be referenced in other kustomization files as:
  resources:
  - oci://registry/repository:tag
`,
		Example: `
# Publish current directory to a registry
kustomize publish ghcr.io/myorg/my-app:v1.0.0

# Publish a specific directory
kustomize publish ghcr.io/myorg/my-app:v1.0.0 --path ./overlays/prod

# Publish to multiple registries
kustomize publish ghcr.io/myorg/my-app:v1.0.0 docker.io/myorg/my-app:v1.0.0 --path ./base
`,
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPublish(fSys, path, source, revision, exclude, args)
		},
	}

	cmd.Flags().StringVarP(&path, "path", "p", "",
		"Path to the kustomization directory to publish. Defaults to current working directory.")
	cmd.Flags().StringVar(&source, "source", "",
		"Source URL to record in OCI annotations (org.opencontainers.image.source).")
	cmd.Flags().StringVar(&revision, "revision", "",
		"Revision to record in OCI annotations (org.opencontainers.image.revision).")
	cmd.Flags().StringArrayVar(&exclude, "exclude", nil,
		"File patterns to exclude from the artifact (gitignore format, repeatable).")

	return cmd
}

func runPublish(fSys filesys.FileSystem, path string, source string, revision string, exclude []string, args []string) error {
	// Parse target references
	targets := make([]name.Tag, 0, len(args))
	for _, arg := range args {
		tag, err := name.NewTag(arg)
		if err != nil {
			return fmt.Errorf("invalid target %q: %w", arg, err)
		}
		if tag.TagStr() == "latest" {
			return fmt.Errorf("target %q must specify an explicit tag (not latest)", arg)
		}
		targets = append(targets, tag)
	}

	// Resolve the kustomization directory
	dir := path
	if dir == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}
		dir = cwd
	}

	// Make path absolute
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving path %q: %w", dir, err)
	}

	if !fSys.IsDir(absDir) {
		return fmt.Errorf("path %q is not a directory", dir)
	}

	// Find and load kustomization file (optional — if present, it will be validated)
	kustomization, _ := loadKustomization(fSys, absDir)

	// Push to registries
	opts := &oci.PushOptions{}
	opts.SetTargets(targets)
	if kustomization != nil {
		opts.SetKustomization(kustomization)
	}
	opts.SetFileSystem(fSys)
	opts.SetRoot(filesys.ConfirmedDir(absDir))

	// Set OCI annotations
	annotations := make(map[string]string)
	if source != "" {
		annotations["org.opencontainers.image.source"] = source
	}
	if revision != "" {
		annotations["org.opencontainers.image.revision"] = revision
	}
	if len(annotations) > 0 {
		opts.SetAnnotations(annotations)
	}
	if len(exclude) > 0 {
		opts.SetExcludePatterns(exclude)
	}

	if err := oci.Push(opts); err != nil {
		return err
	}

	for _, tag := range targets {
		log.Printf("Published to %s\n", tag.Name())
	}
	return nil
}

// loadKustomization finds and reads a kustomization file in the given directory.
func loadKustomization(fSys filesys.FileSystem, dir string) (*types.Kustomization, error) {
	for _, kfilename := range konfig.RecognizedKustomizationFileNames() {
		kpath := filepath.Join(dir, kfilename)
		if fSys.Exists(kpath) {
			content, err := fSys.ReadFile(kpath)
			if err != nil {
				return nil, fmt.Errorf("reading kustomization file: %w", err)
			}
			var kust types.Kustomization
			if err := kust.Unmarshal(content); err != nil {
				return nil, fmt.Errorf("parsing kustomization file: %w", err)
			}
			return &kust, nil
		}
	}
	return nil, fmt.Errorf("no kustomization file found in %q", dir)
}
