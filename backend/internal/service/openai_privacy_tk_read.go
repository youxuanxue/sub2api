package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// openAISettingsUserURL is the read side of the ChatGPT settings resource. Unlike the
// PATCH endpoint (openAISettingsURL), a GET here is NOT Cloudflare-challenged from a
// datacenter egress, so it can confirm the current training_allowed state even when the
// PATCH would be blocked. See disableOpenAITraining for how the two combine.
const openAISettingsUserURL = "https://chatgpt.com/backend-api/settings/user"

// openAIUserSettings is the subset of GET /backend-api/settings/user we depend on.
// TrainingAllowed is a pointer so an absent field is distinguishable from an explicit false.
type openAIUserSettings struct {
	TrainingAllowed *bool `json:"training_allowed"`
}

// readOpenAITrainingDisabled GETs the ChatGPT user settings and reports whether the
// "Improve the model for everyone" toggle (training_allowed) is already off.
//
//	disabled=true,  ok=true  -> upstream confirms training_allowed == false
//	disabled=false, ok=true  -> upstream confirms training_allowed == true
//	ok=false                 -> inconclusive (no token/factory, transport error, non-2xx,
//	                            unparseable body, or field absent); the caller must fall
//	                            back to the PATCH path.
//
// TK: the read path exists because the settings PATCH is Cloudflare-challenged from a
// datacenter egress (privacy_mode=training_set_cf_blocked) even when training is already
// disabled, while this GET is not. Matching the PATCH, "off" is decided on training_allowed
// alone (the only field the PATCH sets), so the read and write stay symmetric.
func readOpenAITrainingDisabled(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) (disabled bool, ok bool) {
	if accessToken == "" || clientFactory == nil {
		return false, false
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Warn("openai_privacy_read_client_error", "error", err.Error())
		return false, false
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		SetHeader("sec-fetch-dest", "empty").
		Get(openAISettingsUserURL)
	if err != nil {
		slog.Warn("openai_privacy_read_request_error", "error", err.Error())
		return false, false
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("openai_privacy_read_non_2xx", "status", resp.StatusCode, "content_type", resp.GetContentType())
		return false, false
	}

	return parseOpenAITrainingDisabled([]byte(resp.String()))
}

// parseOpenAITrainingDisabled is the pure, testable decision: training is "off" only when
// the body parses and training_allowed is present and false. A missing field yields
// ok=false so the caller does not record training_off on an ambiguous read.
func parseOpenAITrainingDisabled(body []byte) (disabled bool, ok bool) {
	var s openAIUserSettings
	if err := json.Unmarshal(body, &s); err != nil {
		return false, false
	}
	if s.TrainingAllowed == nil {
		return false, false
	}
	return !*s.TrainingAllowed, true
}
