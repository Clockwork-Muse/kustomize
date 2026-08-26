// Copyright 2026 The Kubernetes Authors.
// SPDX-License-Identifier: Apache-2.0

package oci

import (
	"net/http"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/stretchr/testify/require"

	loctest "sigs.k8s.io/kustomize/api/testutils/localizertest"
	"sigs.k8s.io/kustomize/kyaml/filesys"
)

func TestKeychainEnvVarAuth(t *testing.T) {
	username := "testuser"
	password := "testpass"

	address, _ := createRegistry(t, username, password, false)

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, username, password, nil)

	// Set env vars for auth
	t.Setenv(EnvOCIUsername, username)
	t.Setenv(EnvOCIPassword, password)
	// Clear DOCKER_CONFIG so DefaultKeychain can't find creds
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, nil)
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}

func TestKeychainEnvVarAuthFailsWithWrongCreds(t *testing.T) {
	username := "testuser"
	password := "testpass"

	address, _ := createRegistry(t, username, password, false)

	// Set wrong env vars
	t.Setenv(EnvOCIUsername, "wronguser")
	t.Setenv(EnvOCIPassword, "wrongpass")
	t.Setenv("DOCKER_CONFIG", t.TempDir())

	repoSpec := RepoSpec{
		Reference: toReference(t, address+"/somerepo:sometag", name.Insecure),
		Dir:       filesys.ConfirmedDir(t.TempDir()),
	}

	err := PullUsingOciManifest(&repoSpec, filesys.MakeFsOnDisk(), nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "UNAUTHORIZED")
}

func TestKeychainFallsBackToDockerConfig(t *testing.T) {
	username := "testuser"
	password := "testpass"

	address, _ := createRegistry(t, username, password, false)

	kustomization := map[string]string{
		"kustomization.yaml": "namePrefix: test-\n",
	}

	src, actual, target := loctest.PrepareFs(t, nil, kustomization)
	reference := toReference(t, address+"/somerepo:sometag", name.Insecure)
	pushArtifact(t, src, target.String(), reference, username, password, nil)

	// Don't set env vars — rely on Docker config
	t.Setenv(EnvOCIUsername, "")
	t.Setenv(EnvOCIPassword, "")
	createDockerConfig(t, address, username, password)

	repoSpec := RepoSpec{
		Reference: reference,
		Dir:       target,
	}

	err := PullUsingOciManifest(&repoSpec, actual, http.DefaultClient)
	require.NoError(t, err)
	loctest.CheckFs(t, target.String(), src, actual)
}
