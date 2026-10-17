package service

import (
	"net/http"
	"strings"
)

func (s *AntigravityGatewayService) appendAntigravitySmartRetryOpsEvent(
	p antigravityRetryLoopParams,
	statusCode int,
	headers http.Header,
	body []byte,
	kind string,
) {
	if p.c == nil || p.account == nil {
		return
	}
	requestID := ""
	if headers != nil {
		requestID = headers.Get("x-request-id")
	}
	appendOpsUpstreamError(p.c, OpsUpstreamErrorEvent{
		ProxyID:            opsUpstreamProxyID(p.account),
		ProxyName:          opsUpstreamProxyName(p.account),
		Platform:           p.account.Platform,
		AccountID:          p.account.ID,
		AccountName:        p.account.Name,
		UpstreamStatusCode: statusCode,
		UpstreamRequestID:  requestID,
		Kind:               kind,
		Message:            sanitizeUpstreamErrorMessage(strings.TrimSpace(extractAntigravityErrorMessage(body))),
		Detail:             s.getUpstreamErrorDetail(body),
	})
}
