package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// The lightweight peek is bounded; candidate preparation decodes through the
// execution owner's body-size limits before planning a request.
const universalPeekMaxCompressedBytes = 256 << 10

// MaybeResolveUniversal retains its historical entry name while preparing the
// shared candidate state for both Direct and Universal inference requests.
// The selected group is a billing origin; execution follows the actual account.
// Explicit ForcePlatform restrictions remain authorization constraints.
// A true return means a protocol-shaped error was written and the request aborted.
func MaybeResolveUniversal(c *gin.Context, apiKey *service.APIKey, resolver *service.UniversalRoutingResolver) bool {
	if resolver == nil || apiKey == nil || (!apiKey.IsUniversal() && !resolver.CandidateSchedulingEnabled()) {
		return false
	}
	ctx := service.WithCandidateIdentity(c.Request.Context(), apiKey.UserID, apiKey.ID)
	if apiKey.IsUniversal() {
		ctx = service.WithUniversalKeyRouting(ctx)
	}
	c.Request = c.Request.WithContext(ctx)
	if deferUniversalWebSocketBilling(c, apiKey) {
		return false
	}

	fullPath := c.FullPath()
	if fullPath == "" && c.Request != nil && c.Request.URL != nil {
		fullPath = c.Request.URL.Path
	}
	method := http.MethodGet
	requestPath := fullPath
	if c.Request != nil {
		method = c.Request.Method
		if c.Request.URL != nil {
			requestPath = c.Request.URL.Path
		}
	}
	if isTokenKeyVideoTaskRead(method, requestPath) {
		// A vt_ poll is a read of an existing registry-owned resource, not a new
		// scheduling decision. VideoFetch authorizes the user and consumes the
		// submit-time pinned upstream route.
		return false
	}

	shape := service.UniversalShapeForRequest(fullPath, method)
	if shape == service.ShapeSkip {
		// 元数据端点（/v1/models、/v1beta/models GET 等）：不解析后端组，让全能 key
		// 以无分组继续（RequireGroupAssignment 已放行 universal key；handler 回落默认）。
		return false
	}

	forcedPlatform := ""
	if c.Request != nil {
		if v, ok := c.Request.Context().Value(ctxkey.ForcePlatform).(string); ok {
			forcedPlatform = strings.TrimSpace(v)
		}
	}

	raw, readErr := readAndRestoreUniversalBody(c)
	if readErr != nil {
		writeUniversalBodyReadError(c, shape, readErr)
		return true
	}
	model := peekUniversalModel(c, shape)
	decodedBody := raw
	if raw != nil {
		// Decode with the execution owner's size limits, on a request copy so raw
		// bytes and Content-Encoding remain intact for downstream handlers.
		copyRequest := c.Request.Clone(c.Request.Context())
		copyRequest.Body = io.NopCloser(bytes.NewReader(raw))
		body, decodeErr := pkghttputil.ReadRequestBodyWithPrealloc(copyRequest)
		if decodeErr != nil {
			writeUniversalBodyReadError(c, shape, decodeErr)
			return true
		}
		if shape == service.ShapeOpenAIChat {
			body, decodeErr = pkghttputil.NormalizeLenientJSONRequestBody(body, 0)
			if decodeErr != nil {
				writeUniversalBodyReadError(c, shape, decodeErr)
				return true
			}
		}
		if shape == service.ShapeAnthropicMessages || shape == service.ShapeAnthropicCountTokens {
			parsed, parseErr := service.ParseGatewayRequest(service.NewRequestBodyRef(body), service.PlatformAnthropic)
			if parseErr == nil {
				if normalized, resolved := service.TkApplyBareModelAlias(forcedPlatform, parsed); resolved != "" {
					body = normalized
					// Planning and forwarding consume the same alias rewrite, including
					// compressed requests. This is independent of group model policy.
					c.Request.Body = io.NopCloser(bytes.NewReader(body))
					c.Request.ContentLength = int64(len(body))
					c.Request.Header.Del("Content-Encoding")
					c.Request.Header.Del("Content-Length")
				}
			}
		}
		if shape != service.ShapeGemini && shape != service.ShapeOpenAIImagesEdit {
			var document struct {
				Model string `json:"model"`
			}
			if json.Unmarshal(body, &document) == nil {
				model = strings.TrimSpace(document.Model)
			}
		}
		ctx := resolver.WithRequest(c.Request.Context(), shape, requestPath, model, body)
		c.Request = c.Request.WithContext(ctx)
		decodedBody = body
	}
	reqLog := universalRoutingLogger(c, apiKey, shape, model, forcedPlatform)

	var backing *service.Group
	var err error
	if model == "" && (shape == service.ShapeOpenAIImages || shape == service.ShapeOpenAIImagesEdit) {
		model = service.DefaultOpenAIImagesModel
	}
	if resolver.CandidateSchedulingEnabled() && model != "" {
		var candidate *service.CandidateRequest
		candidate, err = resolver.PrepareCandidateIngress(c, apiKey, shape, requestPath, model, decodedBody, forcedPlatform)
		if err == nil && candidate != nil {
			observeCandidateBinding(c, candidate)
			backing = apiKey.Group
		}
	} else if apiKey.IsUniversal() {
		backing, err = resolver.Resolve(c.Request.Context(), apiKey, shape, model, forcedPlatform)
	} else {
		return false
	}
	if err != nil {
		// 区分“真没有被授权的组”(403,业务语义) 与跨度加载失败等内部错误(500,可重试):
		// 后者不该被伪装成“该模型不在你的套餐内”。
		if errors.Is(err, service.ErrUniversalUnsupportedModel) {
			reqLog.Warn("universal_routing.unsupported_model")
			writeUniversalRoutingUnsupportedModelError(c, shape, model)
		} else if errors.Is(err, service.ErrUniversalNoEntitledGroup) {
			reqLog.Warn("universal_routing.no_entitled_group")
			writeUniversalRoutingError(c, shape, model)
		} else if errors.Is(err, service.ErrUniversalCapacityUnavailable) {
			reqLog.Warn("universal_routing.capacity_unavailable")
			writeUniversalRoutingCapacityError(c, shape)
		} else if status := infraerrors.Code(err); status >= 400 && status < 500 {
			writeCandidateBillingError(c, shape, err)
		} else {
			reqLog.Error("universal_routing.resolve_failed", zap.Error(err))
			writeUniversalRoutingInternalError(c, shape)
		}
		c.Abort()
		return true
	}
	if backing == nil {
		return false
	}

	// apiKey 是每请求新建的结构（snapshotToAPIKey），替换组但保留 RoutingMode。
	apiKey.Group = backing
	apiKey.GroupID = &backing.ID
	reqLog.Info("universal_routing.resolved",
		zap.Int64("backing_group_id", backing.ID),
		zap.String("backing_group_name", backing.Name),
		zap.String("backing_platform", backing.Platform),
	)
	return false
}

func writeUniversalRoutingUnsupportedModelError(c *gin.Context, shape service.UniversalShape, model string) {
	const status = http.StatusBadRequest
	message := service.TkUnsupportedModelMessage(model)
	service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonUnsupportedModel)
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, message)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		c.JSON(status, gin.H{"type": "error", "error": gin.H{
			"type": "invalid_request_error", "message": message,
		}})
	default:
		c.JSON(status, gin.H{"error": gin.H{
			"message": message,
			"type":    "invalid_request_error",
			"code":    "unsupported_model",
		}})
	}
}

func writeUniversalRoutingCapacityError(c *gin.Context, shape service.UniversalShape) {
	const status = http.StatusTooManyRequests
	const message = "No available accounts for this request. Please retry."
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, message)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, message)
	default:
		c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "rate_limit_error", "code": "no_available_accounts"}})
	}
}

func universalRoutingLogger(c *gin.Context, apiKey *service.APIKey, shape service.UniversalShape, model, forcedPlatform string) *zap.Logger {
	base := logger.L()
	if c != nil && c.Request != nil {
		base = logger.FromContext(c.Request.Context())
	}

	fields := []zap.Field{
		zap.String("component", "middleware.universal_routing"),
		zap.String("universal_shape", universalShapeLabel(shape)),
		zap.String("request_model", strings.TrimSpace(model)),
		zap.String("forced_platform", strings.TrimSpace(forcedPlatform)),
	}
	if apiKey != nil {
		fields = append(fields,
			zap.Int64("api_key_id", apiKey.ID),
			zap.Int64("user_id", apiKey.UserID),
		)
	}
	return base.With(fields...)
}

func universalShapeLabel(shape service.UniversalShape) string {
	switch shape {
	case service.ShapeSkip:
		return "skip"
	case service.ShapeAnthropicMessages:
		return "anthropic_messages"
	case service.ShapeAnthropicCountTokens:
		return "anthropic_count_tokens"
	case service.ShapeOpenAIChat:
		return "openai"
	case service.ShapeOpenAIEmbeddings:
		return "openai_embeddings"
	case service.ShapeOpenAIImages:
		return "openai_images"
	case service.ShapeOpenAIImagesEdit:
		return "openai_images_edit"
	case service.ShapeOpenAIVideo:
		return "openai_video"
	case service.ShapeGemini:
		return "gemini"
	default:
		return "unknown"
	}
}

// peekUniversalModel reads the endpoint's model without consuming its body.
// Gemini names come from the URL; image edits may use multipart fields.
// Submitted video tasks retain their registry-owned route when polled.
func peekUniversalModel(c *gin.Context, shape service.UniversalShape) string {
	switch shape {
	case service.ShapeGemini:
		if ma := c.Param("modelAction"); ma != "" {
			return geminiModelFromAction(ma)
		}
		return c.Param("model")
	case service.ShapeOpenAIImagesEdit:
		return peekImageEditModel(c)
	case service.ShapeOpenAIVideo:
		if c.Request != nil && strings.EqualFold(c.Request.Method, http.MethodPost) {
			return peekModelFromJSONBody(c) // submit：JSON body 含 model
		}
		return "" // poll（GET）：无 body、无需模型
	default:
		return peekModelFromJSONBody(c)
	}
}

// peekModelFromJSONBody 读取请求体、原样还原，再在副本上解码并提取顶层 "model"。
// 关键：还原的是“原始字节 + 原始头”，绝不改动 Content-Encoding/Length，使 handler 后续
// 调用 ReadRequestBodyWithPrealloc 的行为与本中间件未运行时完全一致。
func peekModelFromJSONBody(c *gin.Context) string {
	raw, err := readAndRestoreUniversalBody(c)
	if err != nil {
		return ""
	}
	return extractModelFromJSONBytes(c, raw)
}

// peekImageEditModel reads the model field for /v1/images/edits. Unlike generic
// JSON endpoints, image edits are commonly multipart. The complete bounded raw
// body is buffered; only the small "model" field is extracted from that buffer.
func peekImageEditModel(c *gin.Context) string {
	raw, err := readAndRestoreUniversalBody(c)
	if err != nil {
		return ""
	}
	if model := extractModelFromJSONBytes(c, raw); model != "" {
		return model
	}
	peekBytes := universalPeekBytes(c, raw)
	if len(peekBytes) == 0 {
		return ""
	}
	return extractMultipartModelField(c.GetHeader("Content-Type"), peekBytes)
}

// universalBodyReplay retains both complete bytes and a terminal read failure.
// A later peek must not turn a failed source into a successful truncated body.
type universalBodyReplay struct {
	raw     []byte
	reader  *bytes.Reader
	readErr error
	source  io.Closer
}

func (b *universalBodyReplay) Read(p []byte) (int, error) {
	if b.readErr != nil {
		return 0, b.readErr
	}
	return b.reader.Read(p)
}

func (b *universalBodyReplay) Close() error { return b.source.Close() }

func readAndRestoreUniversalBody(c *gin.Context) ([]byte, error) {
	if c.Request == nil || c.Request.Body == nil {
		return nil, nil
	}
	if body, ok := c.Request.Body.(*universalBodyReplay); ok {
		body.reader.Reset(body.raw)
		return body.raw, body.readErr
	}
	source := c.Request.Body
	raw, err := io.ReadAll(source) // Bounded by the ingress MaxBytesReader.
	if err != nil {
		raw = nil
	}
	c.Request.Body = &universalBodyReplay{raw: raw, reader: bytes.NewReader(raw), readErr: err, source: source}
	return raw, err
}

func writeUniversalBodyReadError(c *gin.Context, shape service.UniversalShape, err error) {
	status, message := http.StatusBadRequest, "Failed to read request body"
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		status, message = http.StatusRequestEntityTooLarge, "Request body too large"
	} else if IsClientClosedRequestError(c, err) {
		status, message = StatusClientClosedRequest, "context canceled"
		service.MarkOpsClientClosedRequest(c)
	}
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, message)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		errorType := "invalid_request_error"
		if status == http.StatusRequestEntityTooLarge {
			errorType = "request_too_large"
		}
		c.JSON(status, gin.H{"type": "error", "error": gin.H{"type": errorType, "message": message}})
	default:
		c.JSON(status, gin.H{"error": gin.H{"message": message, "type": "invalid_request_error"}})
	}
	c.Abort()
}

func extractModelFromJSONBytes(c *gin.Context, raw []byte) string {
	peekBytes := universalPeekBytes(c, raw)
	if len(peekBytes) == 0 {
		return ""
	}
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(peekBytes, &probe)
	return strings.TrimSpace(probe.Model)
}

func universalPeekBytes(c *gin.Context, raw []byte) []byte {
	peekBytes := raw
	if enc := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Encoding"))); enc != "" && enc != "identity" {
		// This bounded peek leaves the complete raw replay intact. Candidate
		// preparation uses the execution decoder for the authoritative payload.
		if len(raw) > universalPeekMaxCompressedBytes {
			return nil
		}
		if decoded, derr := pkghttputil.DecodeContentEncodedBody(enc, raw); derr == nil {
			peekBytes = decoded
		}
	}
	return peekBytes
}

const universalMultipartModelMaxBytes = 8 << 10

func extractMultipartModelField(contentType string, raw []byte) string {
	mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") {
		return ""
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return ""
	}
	reader := multipart.NewReader(bytes.NewReader(raw), boundary)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			return ""
		}
		if err != nil {
			return ""
		}
		if strings.TrimSpace(part.FormName()) != "model" {
			_ = part.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(part, universalMultipartModelMaxBytes+1))
		_ = part.Close()
		if readErr != nil || len(data) > universalMultipartModelMaxBytes {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
}

// geminiModelFromAction 从 "{model}:{action}"（或纯 "{model}"）里取模型名。
func geminiModelFromAction(modelAction string) string {
	s := strings.TrimPrefix(modelAction, "/")
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// writeUniversalRoutingError 按入口协议形状写出“该模型/平台不在你的套餐内”的 403。
func writeUniversalRoutingError(c *gin.Context, shape service.UniversalShape, model string) {
	const status = http.StatusForbidden
	msg := "No platform in your plan can serve this request."
	if model != "" {
		msg = "No platform in your plan can serve model \"" + model + "\"."
	}
	service.MarkOpsClientPolicyDenied(c, service.OpsClientPolicyDeniedReasonAPIKeyGroupUnassigned)

	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, msg)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		AnthropicErrorWriter(c, status, msg)
	default:
		// OpenAI 形状（chat/responses/embeddings/images/video）
		c.JSON(status, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "invalid_request_error",
				"code":    "universal_no_entitled_group",
			},
		})
	}
}

// writeUniversalRoutingInternalError 按入口协议形状写出 500：跨度加载/内部失败,而非授权问题。
// 区别于 writeUniversalRoutingError(403),避免把可重试的服务端错误伪装成“不在你的套餐内”。
func writeUniversalRoutingInternalError(c *gin.Context, shape service.UniversalShape) {
	const status = http.StatusInternalServerError
	const msg = "Failed to prepare authorized candidates for this request. Please retry."
	c.Set(service.OpsRoutingInternalErrorKey, true)
	switch shape {
	case service.ShapeGemini:
		GoogleErrorWriter(c, status, msg)
	case service.ShapeAnthropicMessages, service.ShapeAnthropicCountTokens:
		c.JSON(status, gin.H{
			"type":  "error",
			"error": gin.H{"type": "api_error", "code": "universal_routing_internal_error", "message": msg},
		})
	default:
		c.JSON(status, gin.H{
			"error": gin.H{
				"message": msg,
				"type":    "api_error",
				"code":    "universal_routing_internal_error",
			},
		})
	}
}
