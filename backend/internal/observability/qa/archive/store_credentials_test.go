//go:build unit

package archive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUS062_QAFileIdentityOverridesAmbientAWSOnlyForItsStore(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "ambient-secret")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	identity := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity <- strings.Split(strings.Split(r.Header.Get("Authorization"), "Credential=")[1], "/")[0]
		w.Header().Set("ETag", `"created"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "identity.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"access_key_id":"qa-access","secret_access_key":"qa-secret"}`), 0600))
	storage := config.QACaptureStorageConfig{Driver: "s3", Bucket: "qa", Region: "fsn1", Endpoint: server.URL, ForcePathStyle: true, CredentialsFile: path}
	store, err := NewObjectStoreFromConfig(context.Background(), storage)
	require.NoError(t, err)
	_, err = store.Create(context.Background(), "test", strings.NewReader("body"), 4, "text/plain")
	require.NoError(t, err)
	require.Equal(t, "qa-access", <-identity)
	storage.CredentialsFile = ""
	ambient, err := NewObjectStoreFromConfig(context.Background(), storage)
	require.NoError(t, err)
	_, err = ambient.Create(context.Background(), "test", strings.NewReader("body"), 4, "text/plain")
	require.NoError(t, err)
	require.Equal(t, "ambient-access", <-identity)
	storage.CredentialsFile = path + ".missing"
	_, err = NewObjectStoreFromConfig(context.Background(), storage)
	require.Error(t, err)
	require.NotContains(t, err.Error(), path)
}
