package service

import (
	"context"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

// A relay can reject a model before selecting its own account (for example,
// when its key forbids protocol conversion). That verdict covers this path,
// not every authorized candidate at the main gateway.
func candidateEdgeModelRejection(ctx context.Context, account *Account, status int, headers http.Header, body []byte, model string) *UpstreamFailoverError {
	r := CandidateRequestFromContext(ctx)
	model = strings.TrimSpace(model)
	if r == nil || r.current == nil || r.current.account == nil || account == nil || r.current.account.ID != account.ID ||
		model == "" || status != http.StatusBadRequest ||
		account.Type != AccountTypeAPIKey || !isEdgeMirrorStub(account, edgeIDPattern) {
		return nil
	}
	selectedModel := r.current.model
	if r.current.plan != nil {
		// Forwarders use this immutable plan after applying account aliases.
		selectedModel = r.current.plan.ResolvedModel()
	}
	if selectedModel != model {
		return nil
	}
	if !gjson.ValidBytes(body) || gjson.GetBytes(body, "error.type").String() != TkUnsupportedModelErrType ||
		gjson.GetBytes(body, "error.message").String() != TkUnsupportedModelMessage(model) {
		return nil
	}
	return applyGatewayFailoverSemantic(&UpstreamFailoverError{
		StatusCode: status, ResponseBody: append([]byte(nil), body...), ResponseHeaders: headers.Clone(),
		Scope: GatewayFailureScopeAccount, Reason: "edge_model_path_unavailable", RequestScopedTransient: true,
		ClientStatusCode: http.StatusBadGateway, ClientErrorType: "upstream_error", ClientMessage: "Upstream model path unavailable",
	}, gatewayFailoverProfileGeneric, gatewayFailureSemanticTransientFault)
}
