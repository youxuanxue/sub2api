//go:build unit

package archive

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func TestUS062_HetznerConditionalWritesNormalizeOnlyKnownProvider(t *testing.T) {
	for _, tc := range []struct {
		endpoint string
		hetzner  bool
	}{
		{"https://fsn1.your-objectstorage.com", true},
		{"https://nbg1.your-objectstorage.com/", true},
		{"https://hel1.your-objectstorage.com", true},
		{"", false},
		{"https://s3.us-east-1.amazonaws.com", false},
		{"https://fsn1.your-objectstorage.com.attacker.example", false},
		{"http://fsn1.your-objectstorage.com", false},
	} {
		t.Run(tc.endpoint, func(t *testing.T) {
			require.Equal(t, tc.hetzner, hetznerObjectStorageEndpoint(tc.endpoint))
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				require.Equal(t, http.MethodPut, r.Method)
				require.Empty(t, r.Header.Get("X-Amz-Server-Side-Encryption-Customer-Key"))
				body, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, "next", string(body))
				if r.Header.Get("If-Match") == "stale" {
					w.WriteHeader(http.StatusPreconditionFailed)
					_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code></Error>")
					return
				}
				expected := `"current"`
				if tc.hetzner {
					expected = "current"
				}
				require.Equal(t, expected, r.Header.Get("If-Match"))
				w.Header().Set("ETag", `"next"`)
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			client := s3.NewFromConfig(aws.Config{Region: "fsn1", Credentials: credentials.NewStaticCredentialsProvider("synthetic", "synthetic", "")}, func(o *s3.Options) {
				o.BaseEndpoint = aws.String(server.URL)
				o.UsePathStyle = true
				o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			})
			store := &s3ObjectStore{client: client, bucket: "synthetic", unquoteIfMatch: hetznerObjectStorageEndpoint(tc.endpoint)}
			updated, err := store.CompareAndSwap(context.Background(), "commit.json", `"current"`, strings.NewReader("next"), 4, "application/json")
			require.NoError(t, err)
			require.Equal(t, `"next"`, updated.ETag)
			_, err = store.CompareAndSwap(context.Background(), "commit.json", "stale", strings.NewReader("next"), 4, "application/json")
			require.True(t, errors.Is(err, ErrPreconditionFailed))
			require.Equal(t, 2, requests)
		})
	}
}
