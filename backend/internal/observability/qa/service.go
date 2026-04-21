package qa

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/qarecord"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/alitto/pond/v2"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
)

type Service struct {
	client        *ent.Client
	cfg           config.QACaptureConfig
	store         BlobStore
	pool          pond.Pool
	bodyMaxBytes  int
	retentionDays int
	dlqDir        string
}

func NewService(cfg *config.Config, client *ent.Client) (*Service, error) {
	if cfg == nil || client == nil {
		return &Service{}, nil
	}
	store, err := newBlobStore(cfg.QACapture)
	if err != nil {
		return nil, err
	}
	dataDir := strings.TrimSpace(os.Getenv("DATA_DIR"))
	if dataDir == "" {
		dataDir = "/app/data"
	}
	svc := &Service{
		client:        client,
		cfg:           cfg.QACapture,
		store:         store,
		bodyMaxBytes:  cfg.QACapture.BodyMaxBytes,
		retentionDays: cfg.QACapture.RetentionDays,
		dlqDir:        filepath.Join(dataDir, "qa_dlq"),
	}
	svc.pool = pond.NewPool(cfg.QACapture.WorkerCount, pond.WithQueueSize(cfg.QACapture.QueueSize))
	return svc, nil
}

func (s *Service) Stop() {
	if s == nil || s.pool == nil {
		return
	}
	s.pool.StopAndWait()
}

func (s *Service) Middleware() gin.HandlerFunc {
	return Middleware(s)
}

func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.client != nil && s.store != nil
}

func (s *Service) BodyMaxBytes() int {
	if s == nil || s.bodyMaxBytes <= 0 {
		return 256 * 1024
	}
	return s.bodyMaxBytes
}

func (s *Service) Submit(input CaptureInput) {
	if !s.Enabled() || strings.TrimSpace(input.RequestID) == "" {
		return
	}
	if input.CreatedAt.IsZero() {
		input.CreatedAt = time.Now().UTC()
	}
	if s.pool != nil {
		_, ok := s.pool.TrySubmit(func() {
			_ = s.persistCapture(context.Background(), input)
		})
		if ok {
			return
		}
	}
	_ = s.persistCapture(context.Background(), input)
}

func (s *Service) CaptureFromContext(c *gin.Context) {
	if !s.Enabled() || c == nil {
		return
	}
	apiKey, ok := middleware.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.User == nil || !apiKey.User.QACaptureEnabled || apiKey.QANeverCapture {
		return
	}

	requestID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
	requestBytes, _ := c.Get(contextKeyRequestBytes)
	teeValue, _ := c.Get(contextKeyTeeWriter)
	tee, _ := teeValue.(*teeResponseWriter)

	var requestBody []byte
	if raw, ok := requestBytes.([]byte); ok {
		requestBody = raw
	}
	var responseBody []byte
	var streamChunks []RawSSEChunk
	var responseTruncated bool
	if tee != nil {
		responseBody, streamChunks, responseTruncated = tee.snapshot()
	}
	_ = responseTruncated

	var firstTokenMs *int64
	if value, ok := c.Get("ops_time_to_first_token_ms"); ok {
		if parsed, ok := value.(int64); ok && parsed > 0 {
			firstTokenMs = &parsed
		}
	}

	var accountID *int64
	if value, ok := c.Get("ops_account_id"); ok {
		switch v := value.(type) {
		case int64:
			accountID = &v
		case int:
			tmp := int64(v)
			accountID = &tmp
		}
	}

	platform, _ := c.Request.Context().Value(ctxkey.Platform).(string)
	if platform == "" && apiKey.Group != nil {
		platform = apiKey.Group.Platform
	}
	status := c.Writer.Status()
	durationMs := int64(0)
	if tee != nil {
		durationMs = time.Since(tee.startedAt).Milliseconds()
	}

	input := CaptureInput{
		RequestID:         strings.TrimSpace(requestID),
		UserID:            apiKey.UserID,
		APIKeyID:          apiKey.ID,
		AccountID:         accountID,
		Platform:          strings.TrimSpace(platform),
		RequestedModel:    captureRequestedModel(requestBody),
		InboundEndpoint:   captureInboundEndpoint(c),
		StatusCode:        status,
		DurationMs:        durationMs,
		FirstTokenMs:      firstTokenMs,
		Stream:            captureStreamFlag(c, streamChunks),
		RequestBody:       requestBody,
		ResponseBody:      responseBody,
		ResponseHeaders:   captureResponseHeaders(c),
		StreamChunks:      streamChunks,
		ToolCallsPresent:  captureToolCallsPresent(requestBody),
		MultimodalPresent: captureMultimodalPresent(requestBody),
		Tags:              captureTags(requestBody, responseBody, status, responseTruncated),
		CreatedAt:         time.Now().UTC(),
	}
	s.Submit(input)
}

func (s *Service) persistCapture(ctx context.Context, input CaptureInput) error {
	payload, requestSHA, responseSHA, tags, err := s.buildBlob(input)
	if err != nil {
		return err
	}
	key := s.blobKey(input.CreatedAt, input.RequestID)
	blobURI, err := s.writeBlob(ctx, key, payload, input.RequestID)
	if err != nil {
		return err
	}

	create := s.client.QARecord.Create().
		SetRequestID(input.RequestID).
		SetUserID(input.UserID).
		SetAPIKeyID(input.APIKeyID).
		SetPlatform(captureDefault(input.Platform, "unknown")).
		SetRequestedModel(captureDefault(input.RequestedModel, "")).
		SetInboundEndpoint(captureDefault(input.InboundEndpoint, "")).
		SetStatusCode(input.StatusCode).
		SetDurationMs(input.DurationMs).
		SetStream(input.Stream).
		SetToolCallsPresent(input.ToolCallsPresent).
		SetMultimodalPresent(input.MultimodalPresent).
		SetInputTokens(input.InputTokens).
		SetOutputTokens(input.OutputTokens).
		SetCachedTokens(input.CachedTokens).
		SetRequestSha256(requestSHA).
		SetResponseSha256(responseSHA).
		SetBlobURI(blobURI).
		SetTags(tags).
		SetCreatedAt(input.CreatedAt).
		SetRetentionUntil(input.CreatedAt.Add(time.Duration(s.retentionDays) * 24 * time.Hour))
	if input.AccountID != nil {
		create = create.SetAccountID(*input.AccountID)
	}
	if strings.TrimSpace(input.UpstreamModel) != "" {
		create = create.SetUpstreamModel(strings.TrimSpace(input.UpstreamModel))
	}
	if strings.TrimSpace(input.UpstreamEndpoint) != "" {
		create = create.SetUpstreamEndpoint(strings.TrimSpace(input.UpstreamEndpoint))
	}
	if input.FirstTokenMs != nil {
		create = create.SetFirstTokenMs(*input.FirstTokenMs)
	}
	_, err = create.Save(ctx)
	return err
}

func (s *Service) ExportUserData(ctx context.Context, userID int64, since, until time.Time) (*ExportResult, error) {
	records, err := s.client.QARecord.Query().
		Where(
			qarecord.UserIDEQ(userID),
			qarecord.CreatedAtGTE(since),
			qarecord.CreatedAtLTE(until),
		).
		Order(ent.Asc(qarecord.FieldCreatedAt)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	key := fmt.Sprintf("exports/%d/%d.zip", userID, time.Now().UnixNano())
	tmpFile, err := os.CreateTemp("", "qa-export-*.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmpFile.Name())

	zipWriter := zip.NewWriter(tmpFile)
	indexWriter, err := zipWriter.Create("qa_records.jsonl")
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		row, err := json.Marshal(record)
		if err != nil {
			return nil, err
		}
		if _, err := indexWriter.Write(append(row, '\n')); err != nil {
			return nil, err
		}
	}
	if err := zipWriter.Close(); err != nil {
		return nil, err
	}
	if _, err := tmpFile.Seek(0, 0); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		return nil, err
	}
	if _, err := s.store.Put(ctx, key, body, "application/zip"); err != nil {
		return nil, err
	}
	url, err := s.store.PresignURL(ctx, key, 24*time.Hour)
	if err != nil {
		return nil, err
	}
	return &ExportResult{Key: key, DownloadURL: url}, nil
}

func (s *Service) DeleteUserData(ctx context.Context, userID int64, before *time.Time) (int, error) {
	query := s.client.QARecord.Query().Where(qarecord.UserIDEQ(userID))
	if before != nil {
		query = query.Where(qarecord.CreatedAtLT(*before))
	}
	records, err := query.All(ctx)
	if err != nil {
		return 0, err
	}
	for _, record := range records {
		if record.BlobURI != nil {
			blobURI := strings.TrimSpace(*record.BlobURI)
			if blobURI == "" {
				continue
			}
			if key := s.keyFromBlobURI(blobURI); key != "" {
				_ = s.store.Delete(ctx, key)
			}
		}
	}
	deleteQuery := s.client.QARecord.Delete().Where(qarecord.UserIDEQ(userID))
	if before != nil {
		deleteQuery = deleteQuery.Where(qarecord.CreatedAtLT(*before))
	}
	deleted, err := deleteQuery.Exec(ctx)
	if err != nil {
		return 0, err
	}
	_, _ = s.client.PaymentAuditLog.Create().
		SetOrderID(fmt.Sprintf("qa-data:%d", userID)).
		SetAction("qa_data_delete").
		SetDetail("user requested QA data deletion").
		SetOperator("user").
		SetCreatedAt(time.Now()).
		Save(ctx)
	return deleted, nil
}

func (s *Service) buildBlob(input CaptureInput) ([]byte, string, string, []string, error) {
	requestValue := sanitizeQABytes(input.RequestBody, s.bodyMaxBytes)
	responseValue := sanitizeQABytes(input.ResponseBody, s.bodyMaxBytes)

	chunks := make([]map[string]any, 0, len(input.StreamChunks))
	for _, chunk := range input.StreamChunks {
		chunks = append(chunks, map[string]any{
			"t":       chunk.RecvAtMs,
			"raw_b64": base64.StdEncoding.EncodeToString([]byte(logredact.RedactText(string(chunk.Bytes)))),
		})
	}

	payload := map[string]any{
		"request_id":  input.RequestID,
		"captured_at": input.CreatedAt.Format(time.RFC3339),
		"request": map[string]any{
			"path": input.InboundEndpoint,
			"body": requestValue,
		},
		"response": map[string]any{
			"status_code": input.StatusCode,
			"headers":     input.ResponseHeaders,
			"body":        responseValue,
		},
		"stream": map[string]any{
			"chunks": chunks,
		},
		"redactions": []string{"logredact"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, "", "", nil, err
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, "", "", nil, err
	}
	compressed := enc.EncodeAll(raw, make([]byte, 0, len(raw)))
	requestSHA := sha256Hex(requestValue)
	responseSHA := sha256Hex(responseValue)
	return compressed, requestSHA, responseSHA, dedupeTags(input.Tags), nil
}

func (s *Service) writeBlob(ctx context.Context, key string, payload []byte, requestID string) (string, error) {
	blobURI, err := s.store.Put(ctx, key, payload, "application/zstd")
	if err == nil {
		return blobURI, nil
	}
	if dlqErr := os.MkdirAll(s.dlqDir, 0o755); dlqErr != nil {
		return "", err
	}
	dlqPath := filepath.Join(s.dlqDir, requestID+".json.zst")
	if writeErr := os.WriteFile(dlqPath, payload, 0o644); writeErr != nil {
		return "", err
	}
	return "dlq://" + dlqPath, nil
}

func (s *Service) blobKey(createdAt time.Time, requestID string) string {
	return fmt.Sprintf("%04d/%02d/%02d/%s/%s.json.zst",
		createdAt.Year(),
		int(createdAt.Month()),
		createdAt.Day(),
		requestIDPrefix(requestID),
		requestID,
	)
}

func (s *Service) keyFromBlobURI(blobURI string) string {
	switch {
	case strings.HasPrefix(blobURI, "s3://"):
		parts := strings.SplitN(strings.TrimPrefix(blobURI, "s3://"), "/", 2)
		if len(parts) == 2 {
			return parts[1]
		}
	case strings.HasPrefix(blobURI, "file://"):
		return strings.TrimPrefix(blobURI, "file://")
	}
	return ""
}

func requestIDPrefix(requestID string) string {
	if len(requestID) < 2 {
		return "00"
	}
	return requestID[:2]
}

func sha256Hex(value any) string {
	switch v := value.(type) {
	case string:
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])
	default:
		raw, _ := json.Marshal(v)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:])
	}
}

func sanitizeQABytes(raw []byte, maxBytes int) any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	if maxBytes > 0 && len(raw) > maxBytes {
		raw = raw[:maxBytes]
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return map[string]any{}
	}
	if json.Valid([]byte(trimmed)) {
		var out any
		if err := json.Unmarshal([]byte(logredact.RedactJSON([]byte(trimmed))), &out); err == nil {
			return out
		}
	}
	return logredact.RedactText(trimmed)
}

func captureRequestedModel(body []byte) string {
	return strings.TrimSpace(gjson.GetBytes(body, "model").String())
}

func captureToolCallsPresent(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return gjson.GetBytes(body, "tools").Exists() || gjson.GetBytes(body, "tool_choice").Exists()
}

func captureMultimodalPresent(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	return strings.Contains(string(body), `"image"`) ||
		strings.Contains(string(body), `"audio"`) ||
		strings.Contains(string(body), `"input_image"`)
}

func captureInboundEndpoint(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return c.Request.URL.Path
}

func captureStreamFlag(c *gin.Context, chunks []RawSSEChunk) bool {
	if len(chunks) > 0 {
		return true
	}
	if c == nil {
		return false
	}
	if value, ok := c.Get("ops_stream"); ok {
		if stream, ok := value.(bool); ok {
			return stream
		}
	}
	return false
}

func captureResponseHeaders(c *gin.Context) map[string]string {
	if c == nil {
		return map[string]string{}
	}
	headers := map[string]string{}
	for _, key := range []string{"Content-Type", "User-Agent", "X-Request-ID", "Accept-Encoding"} {
		if value := strings.TrimSpace(c.Writer.Header().Get(key)); value != "" {
			headers[key] = value
		}
	}
	return headers
}

func captureTags(requestBody, responseBody []byte, status int, truncated bool) []string {
	tags := make([]string, 0, 4)
	if status >= 500 {
		tags = append(tags, "error_5xx")
	} else if status >= 400 {
		tags = append(tags, "error_4xx")
	}
	if truncated {
		tags = append(tags, "body_truncated")
	}
	if captureToolCallsPresent(requestBody) {
		tags = append(tags, "tool_calls")
	}
	if captureMultimodalPresent(requestBody) {
		tags = append(tags, "multimodal")
	}
	if len(responseBody) == 0 {
		tags = append(tags, "empty_response")
	}
	return dedupeTags(tags)
}

func captureDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func dedupeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	return out
}
