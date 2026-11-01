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
	"net/http"
	"strconv"
	"strings"

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
	CreateBatch(context.Context, ManagedBatchOwner, batchcontract.BatchSubmitRequest, []batchcontract.RequestLine, string, string) (batchcontract.Batch, error)
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
	return &ManagedBatchHandler{Backend: backend, Enabled: enabled, MaxUploadBytes: 512 << 20, MaxLines: 100000}
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
		if err := c.Request.ParseMultipartForm(32 << 20); err != nil {
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
	lines, err := h.readInputLines(c.Request.Context(), owner, req.InputFileID, req.Endpoint)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	idem := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if len(idem) > 255 {
		writeBatchError(c, badBatch("Idempotency-Key is too long"))
		return
	}
	bodyHash := canonicalBatchHash(req, lines)
	got, err := h.Backend.CreateBatch(c.Request.Context(), owner, req, lines, idem, bodyHash)
	if err != nil {
		writeBatchError(c, err)
		return
	}
	c.JSON(http.StatusOK, got)
}

func (h *ManagedBatchHandler) readInputLines(ctx context.Context, owner ManagedBatchOwner, fileID, endpoint string) ([]batchcontract.RequestLine, error) {
	stream, _, err := h.Backend.OpenFile(ctx, owner, fileID)
	if err != nil {
		return nil, hideBatchNotFound(err)
	}
	defer func() { _ = stream.Close() }()
	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	lines := make([]batchcontract.RequestLine, 0, 128)
	for scanner.Scan() {
		if h.MaxLines > 0 && len(lines) >= h.MaxLines {
			return nil, badBatch("input file has too many lines")
		}
		var line batchcontract.RequestLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			return nil, badBatch("input file is not valid JSONL")
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, badBatch("input file could not be read")
	}
	if err := batchcontract.ValidateRequestLines(lines, endpoint); err != nil {
		return nil, err
	}
	return lines, nil
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

func canonicalBatchHash(req batchcontract.BatchSubmitRequest, lines []batchcontract.RequestLine) string {
	b, _ := json.Marshal(struct {
		Request batchcontract.BatchSubmitRequest `json:"request"`
		Lines   []batchcontract.RequestLine      `json:"lines"`
	}{req, lines})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
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
