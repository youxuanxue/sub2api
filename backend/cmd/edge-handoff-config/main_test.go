package main

import (
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareProtectedIndependentKeys(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bundle")
	m := manifest{Issuer: "https://prod.example", Edges: []edgeSpec{{ID: "e1", Origin: "https://e1.example", AdminUserID: 1, KeyID: "v1"}, {ID: "e2", Origin: "https://e2.example", AdminUserID: 2, KeyID: "v1"}}}
	require.NoError(t, prepare(m, out))
	prod, err := config.LoadEdgeHandoffConfig(filepath.Join(out, "prod.json"))
	require.NoError(t, err)
	require.NotEqual(t, prod.Signers["e1"].Seed, prod.Signers["e2"].Seed)
	edge, err := config.LoadEdgeHandoffConfig(filepath.Join(out, "edge-e1.json"))
	require.NoError(t, err)
	require.Equal(t, int64(1), edge.Receiver.AdminUserID)
	require.Empty(t, edge.Signers)
	info, err := os.Stat(filepath.Join(out, "prod.json"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Error(t, prepare(m, out))
	after, err := config.LoadEdgeHandoffConfig(filepath.Join(out, "prod.json"))
	require.NoError(t, err)
	require.Equal(t, prod.Signers, after.Signers)
}
func TestPrepareRejectsAmbiguousTargets(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bundle")
	e := edgeSpec{ID: "e1", Origin: "https://e1.example", AdminUserID: 1, KeyID: "v1"}
	require.Error(t, prepare(manifest{Issuer: "https://prod.example", Edges: []edgeSpec{e, e}}, out))
	_, err := os.Stat(out)
	require.True(t, os.IsNotExist(err))
}

func TestCheckRequiresValidConfigurationForExpectedRole(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bundle")
	require.Error(t, check(filepath.Join(out, "missing"), "signer"))
	m := manifest{Issuer: "https://prod.example", Edges: []edgeSpec{{ID: "e1", Origin: "https://edge.example", AdminUserID: 1, KeyID: "v1"}}}
	require.NoError(t, prepare(m, out))
	prod := filepath.Join(out, "prod.json")
	edge := filepath.Join(out, "edge-e1.json")
	require.NoError(t, check(prod, "signer"))
	require.NoError(t, check(edge, "receiver"))
	require.Error(t, check(prod, "receiver"))
	require.Error(t, check(edge, "signer"))
	require.Error(t, check(prod, ""))
	require.NoError(t, os.Chmod(prod, 0644))
	require.Error(t, check(prod, "signer"))
	require.NoError(t, os.WriteFile(edge, []byte("malformed"), 0600))
	require.Error(t, check(edge, "receiver"))
}
