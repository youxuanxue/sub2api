package qa

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestCaptureFingerprintsFullInputBeyondSharedPrefix(t *testing.T) {
	var hashes []string
	for _, tail := range []string{"first tail", "different tail"} {
		body := "same prefix" + tail
		r := &requestCaptureReader{ReadCloser: io.NopCloser(strings.NewReader(body)), limit: len("same prefix")}
		got, err := io.ReadAll(r)
		require.NoError(t, err)
		require.Equal(t, body, string(got))
		require.Equal(t, "same prefix", r.body.String())
		d := r.metadata()
		require.True(t, d.WireReadComplete)
		require.True(t, d.WireCaptureTruncated)
		require.Equal(t, int64(len(body)), d.WireBytesRead)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte(body))), d.FullWireSHA256)
		hashes = append(hashes, d.FullWireSHA256)
	}
	require.NotEqual(t, hashes[0], hashes[1], "identical saved prefixes do not identify identical requests")
}

func TestRequestCaptureDoesNotClaimUnreadOrPartialBodyIsComplete(t *testing.T) {
	r := &requestCaptureReader{ReadCloser: io.NopCloser(strings.NewReader("abcdef")), limit: 3}
	require.False(t, r.metadata().WireReadComplete)
	require.Empty(t, r.metadata().FullWireSHA256)
	buf := make([]byte, 3)
	_, err := io.ReadFull(r, buf)
	require.NoError(t, err)
	d := r.metadata()
	require.Equal(t, int64(3), d.WireBytesRead)
	require.False(t, d.WireReadComplete)
	require.Empty(t, d.FullWireSHA256)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256([]byte("abc"))), d.WireReadSHA256)
	// At exactly the capture limit, EOF distinguishes complete from partial.
	r = &requestCaptureReader{ReadCloser: io.NopCloser(strings.NewReader("abc")), limit: 3}
	_, err = io.ReadAll(r)
	require.NoError(t, err)
	require.True(t, r.metadata().WireReadComplete)
	require.False(t, r.metadata().WireCaptureTruncated)
}

func TestRequestCaptureGzipFingerprintIsWireNotDecoded(t *testing.T) {
	body := strings.Repeat("text", 1000)
	var wire bytes.Buffer
	zw := gzip.NewWriter(&wire)
	_, err := io.WriteString(zw, body)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	r := &requestCaptureReader{ReadCloser: io.NopCloser(bytes.NewReader(wire.Bytes())), limit: 16}
	zr, err := gzip.NewReader(r)
	require.NoError(t, err)
	got, err := io.ReadAll(zr)
	require.NoError(t, err)
	require.NoError(t, zr.Close())
	require.Equal(t, body, string(got))
	d := r.metadata()
	require.True(t, d.WireReadComplete)
	require.Equal(t, int64(wire.Len()), d.WireBytesRead)
	require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(wire.Bytes())), d.FullWireSHA256)
	require.NotEqual(t, fmt.Sprintf("%x", sha256.Sum256([]byte(body))), d.FullWireSHA256)
	require.Equal(t, 16, r.body.Len())
}

type failingCaptureReader struct{}

func (failingCaptureReader) Read(p []byte) (int, error) {
	return copy(p, "partial"), io.ErrUnexpectedEOF
}
func (failingCaptureReader) Close() error { return nil }

func TestRequestCaptureReadFailureDoesNotProduceFullHash(t *testing.T) {
	r := &requestCaptureReader{ReadCloser: failingCaptureReader{}, limit: 32}
	got, err := io.ReadAll(r)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.Equal(t, "partial", string(got))
	require.Equal(t, int64(7), r.metadata().WireBytesRead)
	require.Empty(t, r.metadata().FullWireSHA256)
	require.False(t, r.metadata().WireReadComplete)
}
