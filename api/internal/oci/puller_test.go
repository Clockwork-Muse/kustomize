// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	ociTypes "github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/stretchr/testify/require"

	loctest "sigs.k8s.io/kustomize/api/testutils/localizertest"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestPullerNeedsTargets(t *testing.T) {
	err := PullUsingOciManifest(&RepoSpec{}, filesys.MakeEmptyDirInMemory(), nil)
	require.ErrorContains(t, err, "reference is required for pull")
}

func TestPullerCreatesTempDirWhenNotSet(t *testing.T) {
	// Start an in-process registry and push an artifact
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": `namePrefix: test-
`,
	}

	src, _, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, "", "", nil)

	// Create a RepoSpec with the sentinel Dir value (simulating what the loader does)
	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       notPulled, // This is what happens in real usage
	}

	fSys := filesys.MakeFsOnDisk()
	err := PullUsingOciManifest(&repoSpec, fSys, nil)
	require.NoError(t, err)

	// Verify Dir was updated from the sentinel to a real temp directory
	require.NotEqual(t, notPulled, repoSpec.Dir, "Dir should be updated from sentinel value")
	require.DirExists(t, repoSpec.Dir.String())

	// Verify files were pulled into the temp dir
	content, err := os.ReadFile(repoSpec.Dir.Join("kustomization.yaml"))
	require.NoError(t, err)
	require.Contains(t, string(content), "namePrefix: test-")

	// Cleanup
	os.RemoveAll(repoSpec.Dir.String())
}

func TestPullerNeesSomething(t *testing.T) {
	repoSpec := RepoSpec{Reference: name.MustParseReference("localhost:3030/somerepo/someimage:sometag")}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeEmptyDirInMemory(), nil)

	require.Error(t, err)
}

// func TestPusherNeedsNonEmptyKustomization(t *testing.T) {
// 	pushOptions := PushOptions{
// 		kustomization: &types.Kustomization{},
// 		targets:       []reference.NamedTagged{AsNamedTagged("registry.domain/something", "sometag")},
// 	}

// 	err := PushToOciRegistries(&pushOptions)
// 	require.ErrorContains(t, err, "kustomization.yaml is empty")
// }

// func TestPusherNeedsValidMetaIfSet(t *testing.T) {
// 	badData := map[string]types.TypeMeta{
// 		"nonempty_version": {
// 			APIVersion: "NonemptyVersion",
// 		},
// 		"invalid_kind": {
// 			Kind: "InvalidKind",
// 		},
// 		"invalid_version_for_kustomization_kind": {
// 			Kind:       types.KustomizationKind,
// 			APIVersion: "NonemptyVersion",
// 		},
// 		"invalid_version_for_compomenent_kind": {
// 			Kind:       types.ComponentKind,
// 			APIVersion: "NonemptyVersion",
// 		},
// 	}

// 	for name, testCase := range badData {
// 		t.Run(name, func(t *testing.T) {
// 			pushOptions := PushOptions{
// 				kustomization: &types.Kustomization{
// 					TypeMeta:  testCase,
// 					Namespace: "somethingnonempty",
// 				},
// 				targets: []reference.NamedTagged{AsNamedTagged("registry.domain/something", "sometag")},
// 			}

// 			err := PushToOciRegistries(&pushOptions)
// 			require.ErrorContains(t, err, "kustomization has field errors")
// 		})
// 	}
// }

// func TestLogsDeprecatedFields(t *testing.T) {
// 	dummy, _, _ := loctest.PrepareFs(t, []string{}, map[string]string{})

// 	var buf bytes.Buffer
// 	log.SetOutput(&buf)
// 	defer func() {
// 		log.SetOutput(os.Stderr)
// 	}()

// 	pushOptions := PushOptions{
// 		fSys: dummy,
// 		kustomization: &types.Kustomization{
// 			Namespace:    "somethingnonempty",
// 			CommonLabels: map[string]string{"sdfsd": "sdfsf"},
// 			Vars:         []types.Var{{Name: "sdf"}},
// 		},
// 		targets: []reference.NamedTagged{AsNamedTagged("registry.domain/something", "sometag")},
// 	}

// 	_ = PushToOciRegistries(&pushOptions)
// 	require.Contains(t, buf.String(), "Warning: 'commonLabels' is deprecated.")
// 	require.Contains(t, buf.String(), "Warning: 'vars' is deprecated.")
// }

// func TestPullerKustomizationFilePathsMustBeLocalToDirectory(t *testing.T) {
// 	fields := map[string]struct {
// 		fieldName string
// 		factory   func(string) types.Kustomization
// 	}{
// 		"components": {
// 			"Components",
// 			func(p string) types.Kustomization {
// 				return types.Kustomization{
// 					Components: []string{p},
// 				}
// 			},
// 		},
// 		"resources": {
// 			"Resources",
// 			func(p string) types.Kustomization {
// 				return types.Kustomization{
// 					Resources: []string{p},
// 				}
// 			},
// 		},
// 	}
// 	paths := map[string]string{
// 		// "invalid fileurl": "file://asdfsd/something.txt",
// 		"parent directory": "..",
// 	}

// 	for fieldName, generator := range fields {

// 		for pathName, path := range paths {
// 			t.Run(fieldName+"|"+pathName, func(t *testing.T) {
// 				dummy, _, _ := loctest.PrepareFs(t, []string{}, map[string]string{})
// 				kustomization := generator.factory(path)

// 				pushOptions := PushOptions{
// 					fSys:          dummy,
// 					kustomization: &kustomization,
// 					targets:       []reference.NamedTagged{AsNamedTagged("registry.domain/something", "sometag")},
// 				}

// 				err := PushToOciRegistries(&pushOptions)
// 				require.ErrorContains(t, err, "kustomization includes non-local file paths")
// 				require.ErrorContains(t, err, fmt.Sprintf("Path '%s' in element %s is not local", path, generator.fieldName))
// 			})
// 		}
// 	}
// }

func TestPullerUntrustedCertificate(t *testing.T) {
	username := "username"
	password := "password"

	// Explicitly ignoring the certificates
	address, _ := createRegistry(t, username, password, true)

	repoSpec := RepoSpec{Reference: toReference(t, address+"/somerepo/someimage:sometag")}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeEmptyDirInMemory(), http.DefaultClient)

	require.ErrorContains(t, err, "tls: failed to verify certificate: x509: certificate signed by unknown authority")
}

func TestPullerMissingImageNoCredentialFileNoPassword(t *testing.T) {

	address, _ := createRegistry(t, "", "", false)

	repoSpec := RepoSpec{Reference: toReference(t, address+"/somerepo/someimage:sometag", name.Insecure)}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeEmptyDirInMemory(), http.DefaultClient)
	require.ErrorContains(t, err, "NAME_UNKNOWN")
}

func TestPullerNoCredentialFile(t *testing.T) {
	username := "username"
	password := "password"

	address, caCert := createRegistry(t, username, password, true)

	repoSpec := RepoSpec{Reference: toReference(t, address+"/somerepo/someimage:sometag")}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeEmptyDirInMemory(), toClient(caCert))
	require.ErrorContains(t, err, "UNAUTHORIZED")
}

func TestPullerInvalidCredentials(t *testing.T) {
	address, caCert := createRegistry(t, "expectedusername", "expectedpassword", true)
	createDockerConfig(t, address, "actualusername", "actualpassword")

	repoSpec := RepoSpec{Reference: toReference(t, address+"/somerepo/someimage:sometag")}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeEmptyDirInMemory(), toClient(caCert))
	require.ErrorContains(t, err, "UNAUTHORIZED")
}

func TestPullNoPassword(t *testing.T) {

	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": `namePrefix: test-
`,
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, "", "", nil)

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, nil)
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}

func TestPullNoCertificateNoPassword(t *testing.T) {

	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": `namePrefix: test-
`,
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, "", "", nil)

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, nil)
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}

func TestPullNoCertificate(t *testing.T) {
	username := "username"
	password := "password"

	address, _ := createRegistry(t, username, password, false)
	createDockerConfig(t, address, username, password)

	kustomization := map[string]string{
		"kustomization.yaml": `namePrefix: test-
`,
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo/someimage:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, username, password, nil)

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, nil)
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}

func TestPull(t *testing.T) {
	username := "username"
	password := "password"

	address, caCert := createRegistry(t, username, password, true)
	createDockerConfig(t, address, username, password)

	kustomization := map[string]string{
		"kustomization.yaml": `namePrefix: test-
`,
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo/someimage:sometag")
	pushArtifact(t, src, target.String(), reference, username, password, caCert)

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, toClient(caCert))
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}

func TestSemverTagResolution(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}

	src, _, target := loctest.PrepareFs(t, nil, kustomization)

	// Push multiple versions
	for _, tag := range []string{"v1.0.0", "v1.1.0", "v1.2.0", "v2.0.0"} {
		ref := toReference(t, address+"/myorg/semvertest:"+tag, name.Insecure)
		pushArtifact(t, src, target.String(), ref, "", "", nil)
	}

	// Resolve >=1.0.0 <2.0.0 — should pick v1.2.0
	spec, err := NewRepoSpecFromURL("oci://" + address + "/myorg/semvertest:>=1.0.0 <2.0.0")
	require.NoError(t, err)
	require.Equal(t, ">=1.0.0 <2.0.0", spec.SemverConstraint)

	// Pull — should resolve to v1.2.0
	pullDir := t.TempDir()
	spec.Dir = filesys.ConfirmedDir(pullDir)
	err = PullUsingOciManifest(spec, filesys.MakeFsOnDisk(), nil)
	require.NoError(t, err)

	// Verify resolved reference
	require.Contains(t, spec.Reference.String(), "v1.2.0")
}

func TestSemverTagResolutionNoMatch(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}

	src, _, target := loctest.PrepareFs(t, nil, kustomization)

	// Push only v1.x
	ref := toReference(t, address+"/myorg/semvertest2:v1.0.0", name.Insecure)
	pushArtifact(t, src, target.String(), ref, "", "", nil)

	// Try to resolve >=3.0.0 — should fail
	spec, err := NewRepoSpecFromURL("oci://" + address + "/myorg/semvertest2:>=3.0.0")
	require.NoError(t, err)

	spec.Dir = filesys.ConfirmedDir(t.TempDir())
	err = PullUsingOciManifest(spec, filesys.MakeFsOnDisk(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no tag matching semver constraint")
}

func TestPullSelectsKustomizeManifestFromIndex(t *testing.T) {
	address, _ := createRegistry(t, "", "", false)
	createDockerConfig(t, address, "", "")

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}

	src, _, target := loctest.PrepareFs(t, nil, kustomization)

	// Build the kustomize artifact image
	var files []fileEntry
	src.Walk(target.String(), func(path string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		b, _ := src.ReadFile(path)
		relPath, _ := filepath.Rel(target.String(), path)
		files = append(files, fileEntry{name: relPath, content: b})
		return nil
	})

	kustLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return newTarBuffer(files)
	}, tarball.WithMediaType(ociTypes.OCILayer))
	require.NoError(t, err)

	kustImg, err := mutate.AppendLayers(empty.Image, kustLayer)
	require.NoError(t, err)
	kustImg = mutate.MediaType(kustImg, ociTypes.OCIManifestSchema1)
	kustImg = mutate.ConfigMediaType(kustImg, ociTypes.MediaType(KustomizeArtifactType))

	// Build a non-kustomize image (generic OCI image)
	dummyLayer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return newTarBuffer([]fileEntry{{name: "dummy.txt", content: []byte("not kustomize")}})
	}, tarball.WithMediaType(ociTypes.OCILayer))
	require.NoError(t, err)

	otherImg, err := mutate.AppendLayers(empty.Image, dummyLayer)
	require.NoError(t, err)
	otherImg = mutate.MediaType(otherImg, ociTypes.OCIManifestSchema1)
	otherImg = mutate.ConfigMediaType(otherImg, ociTypes.MediaType("application/vnd.oci.image.config.v1+json"))

	// Create an image index containing both
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: otherImg},
		mutate.IndexAddendum{Add: kustImg},
	)
	idx = mutate.IndexMediaType(idx, ociTypes.OCIImageIndex)

	// Push the index
	tag, err := name.NewTag(address+"/myorg/multimanifest:v1.0.0", name.Insecure)
	require.NoError(t, err)
	err = remote.WriteIndex(tag, idx, remote.WithTransport(http.DefaultTransport))
	require.NoError(t, err)

	// Pull — should find the kustomize artifact from the index
	pullDir := t.TempDir()
	repoSpec := RepoSpec{
		Reference: tag,
		Dir:       filesys.ConfirmedDir(pullDir),
	}
	err = PullUsingOciManifest(&repoSpec, filesys.MakeFsOnDisk(), nil)
	require.NoError(t, err)

	// Verify we got the kustomize content, not the dummy
	require.FileExists(t, filepath.Join(pullDir, "kustomization.yaml"))
	content, _ := os.ReadFile(filepath.Join(pullDir, "kustomization.yaml"))
	require.Equal(t, "namePrefix: test-\n", string(content))
	require.NoFileExists(t, filepath.Join(pullDir, "dummy.txt"))
}
