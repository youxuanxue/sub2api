package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
)

// ForwardGeminiViaMessages reuses the native Messages transport, including Kiro,
// credentials, usage, and failover, under the original immutable Gemini Plan.
func (s *GatewayService) ForwardGeminiViaMessages(ctx context.Context, c *gin.Context, account *Account, request protocolrouter.CanonicalRequest, compat *OpenAIGatewayService, groupID *int64) (*ForwardResult, error) {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok || plan.AdapterID() != protocolrouter.AdapterGeminiToMessages {
		return nil, protocolrouter.ErrStalePlan
	}
	converted, err := apicompat.GeminiToMessagesRequest(request.Body(), request.RequestedModel(), request.Profile().Stream)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(converted)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseGatewayRequest(NewRequestBodyRef(body), PlatformAnthropic)
	if err != nil {
		return nil, err
	}
	parsed.GroupID = groupID
	originalWriter, originalRequest := c.Writer, c.Request
	originalParsed, _ := c.Get("parsed_request")
	w := &geminiChatWriter{ResponseWriter: originalWriter, header: make(http.Header), status: http.StatusOK, stream: converted.Stream, messages: &apicompat.MessagesToGeminiStream{}}
	c.Writer = w
	c.Request = originalRequest.Clone(ctx)
	c.Request.URL.Path = "/v1/messages"
	c.Request.URL.RawPath = ""
	c.Request.URL.RawQuery = ""
	c.Set("parsed_request", parsed)
	defer func() {
		c.Writer = originalWriter
		c.Request = originalRequest
		c.Set("parsed_request", originalParsed)
	}()
	var result *ForwardResult
	var forwardErr error
	if IsOpenAICompatPlatform(account.Platform) {
		if compat == nil {
			return nil, ErrProtocolExecutorMissing
		}
		forwarded, err := compat.ForwardAsAnthropic(ctx, c, account, body, "", plan.ResolvedModel())
		result, forwardErr = ForwardResultFromOpenAI(forwarded), err
	} else {
		result, forwardErr = s.Forward(ctx, c, account, parsed)
	}
	if result != nil {
		result.Model = request.RequestedModel()
	}
	if w.err != nil {
		forwardErr = w.err
	}
	if forwardErr == nil {
		forwardErr = w.finish()
	}
	if forwardErr != nil {
		var retry *UpstreamFailoverError
		if !originalWriter.Written() && errors.As(forwardErr, &retry) {
			return result, forwardErr
		}
		status := w.status
		if status < 400 {
			status = http.StatusBadGateway
		}
		if !errors.Is(forwardErr, context.Canceled) {
			w.writeError(status)
		}
		if errors.As(forwardErr, &retry) {
			forwardErr = fmt.Errorf("upstream failed after Gemini output: %v", retry)
		}
		return result, fmt.Errorf("gemini Messages conversion failed: %w", forwardErr)
	}
	return result, nil
}
