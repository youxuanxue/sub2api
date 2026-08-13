package bridge

import (
	"fmt"
	"net/http"

	taskdoubao "github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	newapiservice "github.com/QuantumNous/new-api/service"

	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// TokenKey: XRToken video task adaptor.
//
// XRToken (see internal/integration/newapi/xrtoken.go) resells VolcEngine Ark
// video models behind an ARK-compatible surface. Per its published
// /docs/zh/ark-compatibility contract the request and response BODIES are
// byte-identical to official Ark — only the base URL, the auth header value and
// one path segment differ:
//
//	official Ark : {base}/api/v3/contents/generations/tasks
//	XRToken      : {base}/v1/contents/generations/tasks
//
// Since the differing segment sits in the MIDDLE of the path, no base_url value
// can bend the upstream doubao adaptor onto XRToken's shape (verified live:
// /api/v3/contents/generations/tasks 404s on api.xrtoken.net while
// /v1/contents/generations/tasks answers). And new-api is a pinned upstream we
// must not patch from this repo (CLAUDE.md §4 — "fix the bridge, do NOT modify
// New API"), so the fix belongs here.
//
// Because the bodies match, wrapping is deliberately minimal: embed the upstream
// adaptor and override ONLY the two URL builders. Everything else — request body
// construction, submit/fetch response parsing, OpenAI-Video projection, the
// video-input billing ratios, task status mapping — is inherited unchanged, so
// an upstream fix or Ark schema change flows through automatically instead of
// being frozen into a TK copy.
//
// Auth needs no override: XRToken accepts `Authorization: Bearer <key>`, which
// is exactly what the embedded BuildRequestHeader / FetchTask already send.
type xrTokenTaskAdaptor struct {
	*taskdoubao.TaskAdaptor

	// baseURL mirrors what Init received. The embedded adaptor keeps its own
	// copy in an UNEXPORTED field, so this wrapper cannot read it back and must
	// capture the value itself to build URLs.
	baseURL string
}

// newXRTokenTaskAdaptor wraps a fresh upstream doubao task adaptor.
func newXRTokenTaskAdaptor() *xrTokenTaskAdaptor {
	return &xrTokenTaskAdaptor{TaskAdaptor: &taskdoubao.TaskAdaptor{}}
}

// Init captures the resolved base URL for this wrapper, then delegates so the
// embedded adaptor still initializes its own channel type / api key / base URL.
//
// The captured value is normalized: admins legitimately store XRToken's
// SDK-documented base with a trailing `/v1` (that is the form XRToken's own docs
// hand out), and appending `/v1/contents/...` to it would produce a doubled
// `/v1/v1/...` path. NormalizeXRTokenBaseURL collapses both accepted spellings
// to the canonical root and passes any other host through untouched.
func (a *xrTokenTaskAdaptor) Init(info *relaycommon.RelayInfo) {
	if info != nil {
		a.baseURL = newapiintegration.NormalizeXRTokenBaseURL(info.ChannelBaseUrl)
	}
	a.TaskAdaptor.Init(info)
}

// BuildRequestURL targets XRToken's ARK-compatible submit path.
func (a *xrTokenTaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	if a.baseURL == "" {
		return "", fmt.Errorf("xrtoken video submit: empty base_url")
	}
	return fmt.Sprintf("%s/v1/contents/generations/tasks", a.baseURL), nil
}

// FetchTask polls XRToken's ARK-compatible task-status path.
//
// baseUrl arrives as an argument here (not from Init), because DispatchVideoFetch
// re-resolves it from the persisted task registry — a poll can happen in a
// different process than the submit. Normalize it on the way in for the same
// reason Init does.
func (a *xrTokenTaskAdaptor) FetchTask(baseUrl, key string, body map[string]any, proxy string) (*http.Response, error) {
	taskID, ok := body["task_id"].(string)
	if !ok {
		return nil, fmt.Errorf("invalid task_id")
	}
	base := newapiintegration.NormalizeXRTokenBaseURL(baseUrl)
	if base == "" {
		return nil, fmt.Errorf("xrtoken video fetch: empty base_url")
	}
	uri := fmt.Sprintf("%s/v1/contents/generations/tasks/%s", base, taskID)

	req, err := http.NewRequest(http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+key)

	// Same client resolution the embedded adaptor uses, so account-level proxy
	// configuration keeps working on the poll path.
	client, err := newapiservice.GetHttpClientWithProxy(proxy)
	if err != nil {
		return nil, fmt.Errorf("xrtoken video fetch: new proxy http client failed: %w", err)
	}
	return client.Do(req)
}
