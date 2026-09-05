package service

import (
	"context"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// NormalizeOpenRouterProviderChatBody rewrites tokenkey/<model> requests from
// OpenRouter inference keys back to the internal model id used by scheduling.
func (s *SettingService) NormalizeOpenRouterProviderChatBody(
	ctx context.Context,
	apiKeyID, userID int64,
	keyName string,
	body []byte,
) ([]byte, string, bool, error) {
	if s == nil || len(body) == 0 || !gjson.ValidBytes(body) {
		return body, "", false, nil
	}
	cfg, err := s.GetOpenRouterProviderConfig(ctx)
	if err != nil {
		return body, "", false, err
	}
	if !cfg.AllowsInferenceAPIKey(apiKeyID, userID, keyName) {
		return body, "", false, nil
	}
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || modelResult.Type != gjson.String {
		return body, "", false, nil
	}
	publicModel := strings.TrimSpace(modelResult.String())
	internalModel, ok := cfg.InternalModelID(publicModel)
	if !ok {
		return body, publicModel, false, nil
	}
	rewritten, err := sjson.SetBytes(body, "model", internalModel)
	if err != nil {
		return body, publicModel, false, err
	}
	return rewritten, internalModel, true, nil
}
