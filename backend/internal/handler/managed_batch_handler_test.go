package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type managedBatchBackendStub struct {
	content []byte
	file    ManagedBatchFile
	lines   []batchcontract.RequestLine
	idem    string
	hash    string
}

func (s *managedBatchBackendStub) CreateFile(_ context.Context, _ ManagedBatchOwner, filename, purpose string, size int64, r io.Reader) (ManagedBatchFile, error) {
	b, err := io.ReadAll(r)
	if err != nil {
		return ManagedBatchFile{}, err
	}
	s.content = b
	s.file = ManagedBatchFile{Object: batchcontract.FileObject{ID: "file_1", Object: "file", Filename: filename, Purpose: purpose, Bytes: size, Status: "uploaded"}}
	return s.file, nil
}
func (s *managedBatchBackendStub) GetFile(context.Context, ManagedBatchOwner, string) (ManagedBatchFile, error) {
	return s.file, nil
}
func (s *managedBatchBackendStub) ListFiles(context.Context, ManagedBatchOwner, string, string, int) (batchcontract.FileList, error) {
	return batchcontract.FileList{Object: "list"}, nil
}
func (s *managedBatchBackendStub) OpenFile(context.Context, ManagedBatchOwner, string) (io.ReadCloser, *batchcontract.FileObject, error) {
	return io.NopCloser(bytes.NewReader(s.content)), &s.file.Object, nil
}
func (s *managedBatchBackendStub) DeleteFile(context.Context, ManagedBatchOwner, string) error {
	return nil
}
func (s *managedBatchBackendStub) CreateBatch(_ context.Context, _ ManagedBatchOwner, _ batchcontract.BatchSubmitRequest, lines []batchcontract.RequestLine, idem, hash string) (batchcontract.Batch, error) {
	s.lines, s.idem, s.hash = lines, idem, hash
	return batchcontract.Batch{ID: "batch_1", Object: "batch", Status: "validating"}, nil
}
func (s *managedBatchBackendStub) GetBatch(context.Context, ManagedBatchOwner, string) (batchcontract.Batch, error) {
	return batchcontract.Batch{ID: "batch_1", Object: "batch"}, nil
}
func (s *managedBatchBackendStub) ListBatches(context.Context, ManagedBatchOwner, string, int) (batchcontract.BatchList, error) {
	return batchcontract.BatchList{Object: "list"}, nil
}
func (s *managedBatchBackendStub) CancelBatch(context.Context, ManagedBatchOwner, string) (batchcontract.Batch, error) {
	return batchcontract.Batch{ID: "batch_1", Status: "cancelling"}, nil
}
func (s *managedBatchBackendStub) RetryBatch(context.Context, ManagedBatchOwner, string, string) (batchcontract.Batch, error) {
	return batchcontract.Batch{ID: "batch_2"}, nil
}

func managedBatchTestContext(method, path string, body io.Reader, key *service.APIKey) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, body)
	c.Set(string(middleware.ContextKeyAPIKey), key)
	return c, w
}

func TestManagedBatchHandlerFilesCreateAndContent(t *testing.T) {
	stub := &managedBatchBackendStub{}
	h := NewManagedBatchHandler(stub, true)
	key := &service.APIKey{ID: 7, UserID: 9}
	var body bytes.Buffer
	mw := multipartNew(t, &body, "file", "input.jsonl", "{\"custom_id\":\"a\"}\n")
	require.NoError(t, mw.Close())
	c, w := managedBatchTestContext(http.MethodPost, "/v1/files", &body, key)
	c.Request.Header.Set("Content-Type", mw.FormDataContentType())
	c.Request.Form = nil
	h.FilesCreate(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), `"file_1"`)
	c, w = managedBatchTestContext(http.MethodGet, "/v1/files/file_1/content", nil, key)
	c.Params = gin.Params{{Key: "file_id", Value: "file_1"}}
	h.FilesContent(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, stub.content, w.Body.Bytes())
	require.Equal(t, "application/jsonl", w.Header().Get("Content-Type"))
}

func TestManagedBatchHandlerSubmitValidatesEndpointAndPassesIdempotencyHash(t *testing.T) {
	stub := &managedBatchBackendStub{content: []byte(`{"custom_id":"row-1","method":"POST","url":"/v1/responses","body":{"model":"gpt"}}` + "\n"), file: ManagedBatchFile{Object: batchcontract.FileObject{ID: "file_1", Filename: "input.jsonl"}}}
	h := NewManagedBatchHandler(stub, true)
	key := &service.APIKey{ID: 7, UserID: 9}
	c, w := managedBatchTestContext(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_1","endpoint":"/v1/responses","completion_window":"24h"}`), key)
	c.Params = gin.Params{}
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Idempotency-Key", "same-key")
	h.BatchesCreate(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, stub.lines, 1)
	require.Equal(t, "same-key", stub.idem)
	require.Len(t, stub.hash, 64)
	c, w = managedBatchTestContext(http.MethodPost, "/v1/batches", strings.NewReader(`{"input_file_id":"file_1","endpoint":"/v1/embeddings","completion_window":"24h"}`), key)
	c.Request.Header.Set("Content-Type", "application/json")
	h.BatchesCreate(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "invalid_endpoint")
}

func TestManagedBatchHandlerDisabledByDefault(t *testing.T) {
	h := NewManagedBatchHandler(&managedBatchBackendStub{}, false)
	c, w := managedBatchTestContext(http.MethodGet, "/v1/batches/batch_1", nil, &service.APIKey{ID: 1, UserID: 1})
	c.Params = gin.Params{{Key: "batch_id", Value: "batch_1"}}
	h.BatchesGet(c)
	require.Equal(t, http.StatusNotFound, w.Code)
}

// multipartNew keeps the test independent of handler internals while allowing
// the request to exercise the same multipart parser as production.
func multipartNew(t *testing.T, dst *bytes.Buffer, field, filename, value string) *multipart.Writer {
	t.Helper()
	mw := multipart.NewWriter(dst)
	part, err := mw.CreateFormFile(field, filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(value))
	require.NoError(t, err)
	require.NoError(t, mw.WriteField("purpose", "batch"))
	return mw
}
