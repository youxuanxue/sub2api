package handler

// Managed Batch HTTP surface.  This file deliberately owns only the wire
// contract: persistence, object storage, queueing, provider selection and
// billing are supplied by ManagedBatchBackend and remain outside HTTP code.

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

// ManagedBatchOwner is derived exclusively from the authenticated API key.
// Both values are required so a record cannot be read through another key or
// user.  Backends must treat a mismatch as not-found.
type ManagedBatchOwner struct {
	UserID   int64
	APIKeyID int64
}

type ManagedBatchFile struct {
	Object     batchcontract.FileObject
	StorageRef string
}

// ManagedBatchBackend is the adapter boundary for repository/queue/storage.
// Implementations must enforce owner scope on every method.  OpenFile is used
// for both input validation and result downloads and must return a private
// object stream.
type ManagedBatchBackend interface {
	CreateFile(context.Context, ManagedBatchOwner, string, string, int64, io.Reader) (ManagedBatchFile, error)
	GetFile(context.Context, ManagedBatchOwner, string) (ManagedBatchFile, error)
	ListFiles(context.Context, ManagedBatchOwner, string, string, int) (batchcontract.FileList, error)
	OpenFile(context.Context, ManagedBatchOwner, string) (io.ReadCloser, *batchcontract.FileObject, error)
	DeleteFile(context.Context, ManagedBatchOwner, string) error
	CreateBatch(context.Context, ManagedBatchOwner, batchcontract.BatchSubmitRequest, batchInput, string, string) (batchcontract.Batch, error)
	GetBatch(context.Context, ManagedBatchOwner, string) (batchcontract.Batch, error)
	ListBatches(context.Context, ManagedBatchOwner, string, int) (batchcontract.BatchList, error)
	CancelBatch(context.Context, ManagedBatchOwner, string) (batchcontract.Batch, error)
	RetryBatch(context.Context, ManagedBatchOwner, string, string) (batchcontract.Batch, error)
}

// BatchAPIError lets the backend map storage and state errors to the OpenAI
// error envelope without leaking cross-owner existence.
type BatchAPIError struct {
	Status  int
	Type    string
	Code    string
	Message string
}

// ErrManagedBatchNotFound is the backend sentinel for both a missing resource
// and an owner mismatch.  The HTTP layer deliberately maps both to one 404.
var ErrManagedBatchNotFound = errors.New("managed batch resource not found")

func (e *BatchAPIError) Error() string { return e.Message }

type ManagedBatchHandler struct {
	Backend        ManagedBatchBackend
	Enabled        bool
	MaxUploadBytes int64
	MaxLines       int
}

func NewManagedBatchHandler(backend ManagedBatchBackend, enabled bool) *ManagedBatchHandler {
	return &ManagedBatchHandler{Backend: backend, Enabled: enabled, MaxUploadBytes: 64 << 20, MaxLines: 10000}
}

func (h *ManagedBatchHandler) available(c *gin.Context) bool {
	if h == nil || !h.Enabled || h.Backend == nil {
		c.JSON(http.StatusNotFound, openAIError("not_found", "batch API is not enabled", ""))
		return false
	}
	return true
}

func managedBatchOwner(c *gin.Context) (ManagedBatchOwner, bool) {
	key, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || key == nil || key.ID <= 0 || key.UserID <= 0 {
		return ManagedBatchOwner{}, false
	}
	return ManagedBatchOwner{UserID: key.UserID, APIKeyID: key.ID}, true
}

func (h *ManagedBatchHandler) FilesCreate(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, &BatchAPIError{Status: 401, Type: "authentication_error", Code: "api_key_required", Message: "API key is required"})
		return
	}
	if c.Request.MultipartForm == nil {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, h.MaxUploadBytes+(1<<20))
		if err := c.Request.ParseMultipartForm(1 << 20); err != nil {
			var oversized *http.MaxBytesError
			if errors.As(err, &oversized) {
				writeBatchError(c, &BatchAPIError{Status: 413, Type: "invalid_request_error", Code: "file_too_large", Message: "upload exceeds the maximum size"})
				return
			}
			writeBatchError(c, badBatch("multipart form with file and purpose=batch is required"))
			return
		}
	}
	purpose := c.PostForm("purpose")
	if purpose != batchcontract.FilePurposeBatch {
		writeBatchError(c, badBatch("purpose must be batch"))
		return
	}
	file, header, err := c.Request.FormFile("file")
	if err != nil {
		writeBatchError(c, badBatch("file is required"))
		return
	}
	defer func() { _ = file.Close() }()
	if h.MaxUploadBytes > 0 && header.Size > h.MaxUploadBytes {
		writeBatchError(c, &BatchAPIError{Status: 413, Type: "invalid_request_error", Code: "file_too_large", Message: "file exceeds the maximum size"})
		return
	}
	got, err := h.Backend.CreateFile(c.Request.Context(), owner, header.Filename, purpose, header.Size, file)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, got.Object)
}

func (h *ManagedBatchHandler) FilesGet(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	got, err := h.Backend.GetFile(c.Request.Context(), owner, c.Param("file_id"))
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	c.JSON(http.StatusOK, got.Object)
}

func (h *ManagedBatchHandler) FilesList(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	limit := 10000
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 10000 {
			writeBatchError(c, badBatch("limit must be between 1 and 10000"))
			return
		}
		limit = n
	}
	got, err := h.Backend.ListFiles(c.Request.Context(), owner, c.Query("purpose"), c.Query("after"), limit)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, got)
}

func (h *ManagedBatchHandler) FilesContent(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	stream, file, err := h.Backend.OpenFile(c.Request.Context(), owner, c.Param("file_id"))
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	defer func() { _ = stream.Close() }()
	c.Header("Content-Type", "application/jsonl")
	c.Header("Content-Disposition", `attachment; filename="`+safeFilename(file.Filename)+`"`)
	c.Status(http.StatusOK)
	_, _ = io.Copy(c.Writer, stream)
}

func (h *ManagedBatchHandler) FilesDelete(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	if err := h.Backend.DeleteFile(c.Request.Context(), owner, c.Param("file_id")); err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	c.JSON(http.StatusOK, batchcontract.FileDeleteResponse{ID: c.Param("file_id"), Object: "file", Deleted: true})
}

func (h *ManagedBatchHandler) BatchesCreate(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	var req batchcontract.BatchSubmitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		writeBatchError(c, badBatch("invalid JSON body"))
		return
	}
	if err := batchcontract.ValidateSubmit(req); err != nil {
		writeBatchError(c, err)
		return
	}
	idem := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(idem) > 255 {
		writeBatchError(c, badBatch("Idempotency-Key is too long"))
		return
	}
	stream, _, err := h.Backend.OpenFile(c.Request.Context(), owner, req.InputFileID)
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	defer func() { _ = stream.Close() }()
	file, err := os.CreateTemp("", "batch-input-*")
	if err != nil {
		writeBatchError(c, err)
		return
	}
	defer func() { _ = file.Close() }()
	// The kernel releases this private spool even if the application is killed.
	if err := os.Remove(file.Name()); err != nil {
		writeBatchError(c, err)
		return
	}
	lines, bodyHash, err := stageBatchInput(req, stream, file, h.MaxLines, h.MaxUploadBytes)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	got, err := h.Backend.CreateBatch(c.Request.Context(), owner, req, lines, idem, bodyHash)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, got)
}

// Only IDs and one decoded sample stay in memory. Replay the validated private
// spool inside the submission transaction without holding a DB connection for COS.
type batchInput struct {
	count int
	rows  iter.Seq2[batchcontract.RequestLine, error]
}

func stageBatchInput(req batchcontract.BatchSubmitRequest, src io.Reader, file *os.File, maxLines int, maxBytes int64) (batchInput, string, error) {
	var empty batchInput
	limited := &io.LimitedReader{R: src, N: maxBytes + 1}
	scan := bufio.NewScanner(limited)
	scan.Buffer(make([]byte, 64<<10), 4<<20)
	hash := sha256.New()
	reqJSON, err := json.Marshal(req)
	if err != nil {
		return empty, "", err
	}
	_, _ = io.WriteString(hash, `{"request":`)
	_, _ = hash.Write(reqJSON)
	_, _ = io.WriteString(hash, `,"lines":[`)
	seen := map[string]struct{}{}
	w := bufio.NewWriter(file)
	for scan.Scan() {
		if len(seen) >= maxLines {
			return empty, "", badBatch("input file has too many lines")
		}
		var line batchcontract.RequestLine
		if err := json.Unmarshal(scan.Bytes(), &line); err != nil {
			return empty, "", badBatch("input file is not valid JSONL")
		}
		if err := batchcontract.ValidateRequestLines([]batchcontract.RequestLine{line}, req.Endpoint); err != nil {
			return empty, "", err
		}
		if utf8.RuneCountInString(line.CustomID) > 255 {
			return empty, "", badBatch("custom_id exceeds 255 characters")
		}
		if _, dup := seen[line.CustomID]; dup {
			return empty, "", &BatchAPIError{Status: 400, Type: "invalid_request_error", Code: batchcontract.ErrorDuplicateCustomID, Message: "custom_id must be unique"}
		}
		if len(seen) > 0 {
			_, _ = io.WriteString(hash, ",")
		}
		seen[line.CustomID] = struct{}{}
		raw, err := json.Marshal(line)
		if err != nil {
			return empty, "", err
		}
		_, _ = hash.Write(raw)
		if _, err = w.Write(raw); err != nil {
			return empty, "", err
		}
		if err = w.WriteByte('\n'); err != nil {
			return empty, "", err
		}
	}
	if scan.Err() != nil || limited.N == 0 || len(seen) == 0 {
		return empty, "", badBatch("invalid, oversized, or empty input file")
	}
	if err = w.Flush(); err != nil {
		return empty, "", err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return empty, "", err
	}
	_, _ = io.WriteString(hash, "]}")
	return batchInput{len(seen), func(yield func(batchcontract.RequestLine, error) bool) {
		dec := json.NewDecoder(file)
		for {
			var line batchcontract.RequestLine
			err := dec.Decode(&line)
			if err == io.EOF {
				return
			}
			if !yield(line, err) || err != nil {
				return
			}
		}
	}}, hex.EncodeToString(hash.Sum(nil)), nil
}

func (h *ManagedBatchHandler) BatchesList(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	limit := 20
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			writeBatchError(c, badBatch("limit must be between 1 and 100"))
			return
		}
		limit = n
	}
	got, err := h.Backend.ListBatches(c.Request.Context(), owner, c.Query("after"), limit)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, got)
}

func (h *ManagedBatchHandler) BatchesGet(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	got, err := h.Backend.GetBatch(c.Request.Context(), owner, c.Param("batch_id"))
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	c.JSON(http.StatusOK, got)
}

func (h *ManagedBatchHandler) BatchesCancel(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	got, err := h.Backend.CancelBatch(c.Request.Context(), owner, c.Param("batch_id"))
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	c.JSON(http.StatusOK, got)
}

func (h *ManagedBatchHandler) BatchesRetry(c *gin.Context) {
	if !h.available(c) {
		return
	}
	owner, ok := managedBatchOwner(c)
	if !ok {
		writeBatchError(c, authBatch())
		return
	}
	got, err := h.Backend.RetryBatch(c.Request.Context(), owner, c.Param("batch_id"), strings.TrimSpace(c.GetHeader("Idempotency-Key")))
	if err != nil {
		writeBatchError(c, hideBatchNotFound(err))
		return
	}
	c.JSON(http.StatusOK, got)
}

func safeFilename(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "content.jsonl"
	}
	return strings.NewReplacer("\"", "", "\r", "", "\n", "").Replace(name)
}

func authBatch() error {
	return &BatchAPIError{Status: 401, Type: "authentication_error", Code: "api_key_required", Message: "API key is required"}
}
func badBatch(message string) error {
	return &BatchAPIError{Status: 400, Type: "invalid_request_error", Code: "invalid_request", Message: message}
}

func hideBatchNotFound(err error) error {
	if errors.Is(err, ErrManagedBatchNotFound) {
		return &BatchAPIError{Status: 404, Type: "invalid_request_error", Code: "not_found", Message: "resource not found"}
	}
	var apiErr *BatchAPIError
	if errors.As(err, &apiErr) && (apiErr.Status == 403 || apiErr.Status == 404) {
		return &BatchAPIError{Status: 404, Type: "invalid_request_error", Code: "not_found", Message: "resource not found"}
	}
	return err
}

func openAIError(code, message, typ string) gin.H {
	if typ == "" {
		typ = "invalid_request_error"
	}
	return gin.H{"error": gin.H{"message": message, "type": typ, "param": nil, "code": code}}
}
func writeBatchError(c *gin.Context, err error) {
	if err == nil {
		err = &BatchAPIError{Status: 500, Type: "server_error", Code: "internal_error", Message: "internal error"}
	}
	status := 500
	typ, code, msg := "server_error", "internal_error", "internal error"
	var apiErr *BatchAPIError
	if errors.As(err, &apiErr) {
		if apiErr.Status > 0 {
			status = apiErr.Status
		}
		if apiErr.Type != "" {
			typ = apiErr.Type
		}
		if apiErr.Code != "" {
			code = apiErr.Code
		}
		if apiErr.Message != "" {
			msg = apiErr.Message
		}
	} else if ce, ok := err.(*batchcontract.ContractError); ok {
		status = 400
		typ = "invalid_request_error"
		code, msg = ce.Code, ce.Message
	}
	c.JSON(status, openAIError(code, msg, typ))
}
