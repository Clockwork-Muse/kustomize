// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"path/filepath"

	"sigs.k8s.io/kustomize/kyaml/filesys"
	"os"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/stretchr/testify/require"

	loctest "sigs.k8s.io/kustomize/api/testutils/localizertest"
	"sigs.k8s.io/kustomize/api/types"
)

func mustTag(repo string, tag string) name.Tag {
	ref, err := name.NewTag(repo+":"+tag, name.Insecure)
	if err != nil {
		panic(err)
	}
	return ref
}

// TestPusherNeedsTargets verifies that PushToOciRegistries returns an error
// when no target registry tags are provided in the push options.
func TestPusherNeedsTargets(t *testing.T) {
	err := PushToOciRegistries(&PushOptions{})
	require.ErrorContains(t, err, "At least one target is required.")
}

// TestPusherAllowsNilKustomization verifies that a push succeeds when no
// kustomization file is present — allowing raw directories to be published.
func TestPusherAllowsNilKustomization(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(filepath.Join(dir, "deployment.yaml"), []byte("apiVersion: apps/v1\n"))

	tag, _ := name.NewTag(address+"/myorg/notkust:v1.0.0", name.Insecure)
	pushOptions := PushOptions{}
	pushOptions.SetTargets([]name.Tag{tag})
	pushOptions.SetFileSystem(fSys)
	pushOptions.SetRoot(filesys.ConfirmedDir(dir))

	err := PushToOciRegistries(&pushOptions)
	require.NoError(t, err)
}

// TestPusherNeedsNonEmptyKustomization verifies that pushing fails when a
// kustomization is provided but has no meaningful content (all fields empty).
func TestPusherNeedsNonEmptyKustomization(t *testing.T) {
	pushOptions := PushOptions{
		kustomization: &types.Kustomization{},
		targets:       []name.Tag{mustTag("registry.domain/something", "sometag")},
	}

	err := PushToOciRegistries(&pushOptions)
	require.ErrorContains(t, err, "kustomization.yaml is empty")
}

// TestPusherNeedsValidMetaIfSet verifies that pushing fails when the kustomization
// has invalid TypeMeta (e.g. wrong APIVersion, invalid Kind). Tests multiple
// combinations of bad metadata fields.
func TestPusherNeedsValidMetaIfSet(t *testing.T) {
	badData := map[string]types.TypeMeta{
		"nonempty_version": {
			APIVersion: "NonemptyVersion",
		},
		"invalid_kind": {
			Kind: "InvalidKind",
		},
		"invalid_version_for_kustomization_kind": {
			Kind:       types.KustomizationKind,
			APIVersion: "NonemptyVersion",
		},
		"invalid_version_for_compomenent_kind": {
			Kind:       types.ComponentKind,
			APIVersion: "NonemptyVersion",
		},
	}

	for testName, testCase := range badData {
		t.Run(testName, func(t *testing.T) {
			pushOptions := PushOptions{
				kustomization: &types.Kustomization{
					TypeMeta:  testCase,
					Namespace: "somethingnonempty",
				},
				targets: []name.Tag{mustTag("registry.domain/something", "sometag")},
			}

			err := PushToOciRegistries(&pushOptions)
			require.ErrorContains(t, err, "kustomization has field errors")
		})
	}
}

// TestLogsDeprecatedFields verifies that deprecation warnings are logged when
// the kustomization uses deprecated fields like commonLabels and vars.
func TestLogsDeprecatedFields(t *testing.T) {
	dummy, _, _ := loctest.PrepareFs(t, []string{}, map[string]string{})

	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer func() {
		log.SetOutput(os.Stderr)
	}()

	pushOptions := PushOptions{
		fSys: dummy,
		kustomization: &types.Kustomization{
			Namespace:    "somethingnonempty",
			CommonLabels: map[string]string{"sdfsd": "sdfsf"},
			Vars:         []types.Var{{Name: "sdf"}},
		},
		targets: []name.Tag{mustTag("registry.domain/something", "sometag")},
	}

	_ = PushToOciRegistries(&pushOptions)
	require.Contains(t, buf.String(), "Warning: 'commonLabels' is deprecated.")
	require.Contains(t, buf.String(), "Warning: 'vars' is deprecated.")
}

// TestKustomizationFilePathsMustBeLocalToDirectory verifies that pushing fails when
// any kustomization field (resources, components, patches, generators, etc.) references
// a path outside the kustomization directory (e.g. ".."). This prevents publishing
// artifacts that depend on files outside the bundle.
func TestKustomizationFilePathsMustBeLocalToDirectory(t *testing.T) {
	fields := map[string]struct {
		fieldName string
		factory   func(string) types.Kustomization
	}{
		"components": {
			"Components",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Components: []string{p},
				}
			},
		},
		"resources": {
			"Resources",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Resources: []string{p},
				}
			},
		},
		"crds": {
			"Crds",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Crds: []string{p},
				}
			},
		},
		"configurations": {
			"Configurations",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Configurations: []string{p},
				}
			},
		},
		"generators": {
			"Generators",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Generators: []string{p},
				}
			},
		},
		"transformers": {
			"Transformers",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Transformers: []string{p},
				}
			},
		},
		"validators": {
			"Validators",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Validators: []string{p},
				}
			},
		},
		"patches": {
			"Patches",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Patches: []types.Patch{{Path: p}},
				}
			},
		},
		"replacements": {
			"Replacements",
			func(p string) types.Kustomization {
				return types.Kustomization{
					Replacements: []types.ReplacementField{{Path: p}},
				}
			},
		},
		"configMapGenerator files": {
			"ConfigMapGenerator",
			func(p string) types.Kustomization {
				return types.Kustomization{
					ConfigMapGenerator: []types.ConfigMapArgs{{GeneratorArgs: types.GeneratorArgs{KvPairSources: types.KvPairSources{FileSources: []string{p}}}}},
				}
			},
		},
		"configMapGenerator envs": {
			"ConfigMapGenerator",
			func(p string) types.Kustomization {
				return types.Kustomization{
					ConfigMapGenerator: []types.ConfigMapArgs{{GeneratorArgs: types.GeneratorArgs{KvPairSources: types.KvPairSources{EnvSources: []string{p}}}}},
				}
			},
		},
		"secretGenerator files": {
			"SecretGenerator",
			func(p string) types.Kustomization {
				return types.Kustomization{
					SecretGenerator: []types.SecretArgs{{GeneratorArgs: types.GeneratorArgs{KvPairSources: types.KvPairSources{FileSources: []string{p}}}}},
				}
			},
		},
		"SecretGenerator envs": {
			"SecretGenerator",
			func(p string) types.Kustomization {
				return types.Kustomization{
					SecretGenerator: []types.SecretArgs{{GeneratorArgs: types.GeneratorArgs{KvPairSources: types.KvPairSources{EnvSources: []string{p}}}}},
				}
			},
		},
		"helmCharts valuesFile": {
			"HelmCharts",
			func(p string) types.Kustomization {
				return types.Kustomization{
					HelmCharts: []types.HelmChart{{ValuesFile: p}},
				}
			},
		},
		"helmCharts additionalValuesFile": {
			"HelmCharts",
			func(p string) types.Kustomization {
				return types.Kustomization{
					HelmCharts: []types.HelmChart{{AdditionalValuesFiles: []string{p}}},
				}
			},
		},
	}
	paths := map[string]string{
		// "invalid fileurl": "file://asdfsd/something.txt",
		"parent directory": "..",
	}

	for fieldName, generator := range fields {

		for pathName, path := range paths {
			t.Run(fieldName+"|"+pathName, func(t *testing.T) {
				dummy, _, _ := loctest.PrepareFs(t, []string{}, map[string]string{})
				kustomization := generator.factory(path)

				pushOptions := PushOptions{
					fSys:          dummy,
					kustomization: &kustomization,
					targets:       []name.Tag{mustTag("registry.domain/something", "sometag")},
				}

				err := PushToOciRegistries(&pushOptions)
				require.ErrorContains(t, err, "kustomization includes non-local file paths")
				require.ErrorContains(t, err, fmt.Sprintf("Path '%s' in element %s is not local", path, generator.fieldName))
			})
		}
	}
}

// TestUntrustedCertificate verifies that pushing to a registry with a self-signed
// TLS certificate fails with a certificate verification error when no CA is configured.
func TestUntrustedCertificate(t *testing.T) {
	username := "username"
	password := "password"

	// Explicitly ignoring the certificates
	address, _ := createRegistry(t, username, password, true)
	kustomization := map[string]string{
		"src/kustomization.yaml": `namePrefix: test-
`,
	}

	_, actual, target := loctest.PrepareFs(t, []string{"src"}, kustomization)
	loctest.SetWorkingDir(t, target.Join("src"))

	pushOptions := PushOptions{
		fSys: actual,
		kustomization: &types.Kustomization{
			Namespace: "somethingnonempty",
		},
		targets: []name.Tag{mustTag(address+"/something", "sometag")},
		http:    http.DefaultClient,
	}

	err := PushToOciRegistries(&pushOptions)
	require.ErrorContains(t, err, "tls: failed to verify certificate: x509: certificate signed by unknown authority")
}

// TestNoCredentialFile verifies that pushing to a registry that requires
// authentication fails with 401 Unauthorized when no Docker credentials are configured.
func TestNoCredentialFile(t *testing.T) {
	username := "username"
	password := "password"

	address, caCert := createRegistry(t, username, password, true)

	kustomization := map[string]string{
		"src/kustomization.yaml": `namePrefix: test-
`,
	}

	_, actual, target := loctest.PrepareFs(t, []string{"src"}, kustomization)
	loctest.SetWorkingDir(t, target.Join("src"))

	pushOptions := PushOptions{
		fSys: actual,
		kustomization: &types.Kustomization{
			Namespace: "somethingnonempty",
		},
		targets: []name.Tag{mustTag(address+"/something", "sometag")},
		http:    toClient(caCert),
	}

	err := PushToOciRegistries(&pushOptions)
	require.ErrorContains(t, err, "401 Unauthorized")
}

// TestInvalidCredentials verifies that pushing fails with 401 Unauthorized when
// the Docker config contains incorrect username/password for the target registry.
func TestInvalidCredentials(t *testing.T) {
	address, caCert := createRegistry(t, "expectedusername", "expectedpassword", true)
	createDockerConfig(t, address, "actualusername", "actualpassword")

	kustomization := map[string]string{
		"src/kustomization.yaml": `namePrefix: test-
`,
	}

	_, actual, target := loctest.PrepareFs(t, []string{"src"}, kustomization)
	loctest.SetWorkingDir(t, target.Join("src"))

	pushOptions := PushOptions{
		fSys: actual,
		kustomization: &types.Kustomization{
			Namespace: "somethingnonempty",
		},
		targets: []name.Tag{mustTag(address+"/something", "sometag")},
		http:    toClient(caCert),
	}

	err := PushToOciRegistries(&pushOptions)
	require.ErrorContains(t, err, "401 Unauthorized")
}

// TestPush verifies the happy path: pushing a valid kustomization to an
// authenticated TLS registry succeeds without error.
func TestPush(t *testing.T) {
	username := "username"
	password := "password"

	address, caCert := createRegistry(t, username, password, true)
	createDockerConfig(t, address, username, password)

	kustomization := map[string]string{
		"src/kustomization.yaml": `namePrefix: test-
`,
	}

	_, actual, target := loctest.PrepareFs(t, []string{"src"}, kustomization)
	loctest.SetWorkingDir(t, target.Join("src"))

	pushOptions := PushOptions{
		fSys: actual,
		kustomization: &types.Kustomization{
			Namespace: "somethingnonempty",
		},
		targets: []name.Tag{mustTag(address+"/something", "sometag")},
		http:    toClient(caCert),
	}

	err := PushToOciRegistries(&pushOptions)
	require.NoError(t, err)
}

// TestPushSetsAnnotations verifies that OCI manifest annotations (source, revision,
// created timestamp) are correctly set on the pushed image manifest when configured.
func TestPushSetsAnnotations(t *testing.T) {
	username := "username"
	password := "password"

	address, caCert := createRegistry(t, username, password, true)
	createDockerConfig(t, address, username, password)

	kustomization := map[string]string{
		"src/kustomization.yaml": "namePrefix: test-\n",
	}

	_, actual, target := loctest.PrepareFs(t, []string{"src"}, kustomization)
	loctest.SetWorkingDir(t, target.Join("src"))

	tag, _ := name.NewTag(address+"/myorg/myapp:v1.0.0", name.Insecure)
	opts := PushOptions{}
	opts.SetTargets([]name.Tag{tag})
	opts.SetKustomization(&types.Kustomization{Namespace: "test"})
	opts.SetFileSystem(actual)
	opts.SetHTTPClient(toClient(caCert))
	opts.SetAnnotations(map[string]string{
		"org.opencontainers.image.source":   "https://github.com/org/repo",
		"org.opencontainers.image.revision": "abc123def",
	})

	err := PushToOciRegistries(&opts)
	require.NoError(t, err)

	// Pull back and verify annotations
	img, err := remote.Image(tag, remote.WithTransport(toClient(caCert).Transport), remote.WithAuthFromKeychain(Keychain()))
	require.NoError(t, err)

	manifest, err := img.Manifest()
	require.NoError(t, err)

	require.Contains(t, manifest.Annotations, "org.opencontainers.image.created")
	require.Equal(t, "https://github.com/org/repo", manifest.Annotations["org.opencontainers.image.source"])
	require.Equal(t, "abc123def", manifest.Annotations["org.opencontainers.image.revision"])
}

// TestPushExcludesFilesMatchingKustomizeignore verifies that files matching patterns
// in .kustomizeignore are excluded from the pushed artifact. Also verifies that
// .kustomizeignore itself is excluded from the artifact.
func TestPushExcludesFilesMatchingKustomizeignore(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	// Create a dir with .kustomizeignore
	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("namespace: test\n"))
	fSys.WriteFile(filepath.Join(dir, "deployment.yaml"), []byte("apiVersion: apps/v1\n"))
	fSys.WriteFile(filepath.Join(dir, "README.md"), []byte("# readme\n"))
	fSys.WriteFile(filepath.Join(dir, "notes.txt"), []byte("notes\n"))
	fSys.WriteFile(filepath.Join(dir, ".kustomizeignore"), []byte("*.md\n*.txt\n"))

	tag, _ := name.NewTag(address+"/myorg/excluded:v1.0.0", name.Insecure)
	opts := PushOptions{}
	opts.SetTargets([]name.Tag{tag})
	opts.SetKustomization(&types.Kustomization{Namespace: "test"})
	opts.SetFileSystem(fSys)
	opts.SetRoot(filesys.ConfirmedDir(dir))

	err := PushToOciRegistries(&opts)
	require.NoError(t, err)

	// Pull and extract to verify excluded files are absent
	repoSpec := RepoSpec{
		Reference: tag,
		Dir:       filesys.ConfirmedDir(t.TempDir()),
	}
	err = PullUsingOciManifest(&repoSpec, filesys.MakeFsOnDisk(), nil)
	require.NoError(t, err)

	// kustomization.yaml and deployment.yaml should be present
	require.FileExists(t, filepath.Join(repoSpec.Dir.String(), "kustomization.yaml"))
	require.FileExists(t, filepath.Join(repoSpec.Dir.String(), "deployment.yaml"))
	// README.md and notes.txt should be excluded
	require.NoFileExists(t, filepath.Join(repoSpec.Dir.String(), "README.md"))
	require.NoFileExists(t, filepath.Join(repoSpec.Dir.String(), "notes.txt"))
	// .kustomizeignore itself should also be excluded from the artifact
	require.NoFileExists(t, filepath.Join(repoSpec.Dir.String(), ".kustomizeignore"))
}

// TestPushExcludesFilesWithExcludeFlag verifies that files matching patterns
// provided via the --exclude flag are excluded from the pushed artifact.
func TestPushExcludesFilesWithExcludeFlag(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	dir := t.TempDir()
	fSys := filesys.MakeFsOnDisk()
	fSys.WriteFile(filepath.Join(dir, "kustomization.yaml"), []byte("namespace: test\n"))
	fSys.WriteFile(filepath.Join(dir, "deployment.yaml"), []byte("apiVersion: apps/v1\n"))
	fSys.WriteFile(filepath.Join(dir, "secret.env"), []byte("PASSWORD=foo\n"))

	tag, _ := name.NewTag(address+"/myorg/excluded2:v1.0.0", name.Insecure)
	opts := PushOptions{}
	opts.SetTargets([]name.Tag{tag})
	opts.SetKustomization(&types.Kustomization{Namespace: "test"})
	opts.SetFileSystem(fSys)
	opts.SetRoot(filesys.ConfirmedDir(dir))
	opts.SetExcludePatterns([]string{"*.env"})

	err := PushToOciRegistries(&opts)
	require.NoError(t, err)

	// Pull and verify
	repoSpec := RepoSpec{
		Reference: tag,
		Dir:       filesys.ConfirmedDir(t.TempDir()),
	}
	err = PullUsingOciManifest(&repoSpec, filesys.MakeFsOnDisk(), nil)
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(repoSpec.Dir.String(), "kustomization.yaml"))
	require.FileExists(t, filepath.Join(repoSpec.Dir.String(), "deployment.yaml"))
	require.NoFileExists(t, filepath.Join(repoSpec.Dir.String(), "secret.env"))
}
