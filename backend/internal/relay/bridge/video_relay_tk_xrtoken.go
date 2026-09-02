package bridge

import (
	"bytes"
	"fmt"
	"io"
	"net/http"

	newapichannel "github.com/QuantumNous/new-api/relay/channel"
	taskdoubao "github.com/QuantumNous/new-api/relay/channel/task/doubao"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	newapiservice "github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

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
// adaptor, override the two URL builders, and override DoRequest only to preserve
// virtual dispatch to this wrapper's submit URL. Go's promoted-method semantics
// bind the embedded TaskAdaptor.DoRequest receiver to the inner adaptor, which
// would otherwise call its own `/api/v3/` BuildRequestURL and bypass this wrapper.
// Request-body construction and normalization, submit/fetch response parsing,
// OpenAI-Video projection, video-input billing ratios, and task-status mapping
// still delegate to the upstream adaptor so upstream fixes continue to flow.
//
// Auth needs no header override: XRToken accepts `Authorization: Bearer <key>`,
// which is exactly what the embedded BuildRequestHeader sends.
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

// DoRequest preserves method dispatch to this wrapper's BuildRequestURL.
//
// Calling the promoted TaskAdaptor.DoRequest is not sufficient: that method
// passes its embedded receiver to DoTaskApiRequest, so Go resolves
// BuildRequestURL on *taskdoubao.TaskAdaptor and silently restores `/api/v3/`.
// Passing the outer adaptor keeps the inherited request-header behavior while
// ensuring the submit uses XRToken's `/v1/` path.
func (a *xrTokenTaskAdaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (*http.Response, error) {
	return newapichannel.DoTaskApiRequest(a, c, info, requestBody)
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

// BuildRequestBody delegates to the embedded Ark payload builder, then rewrites
// exactly one field: `model` gains XRToken's vendor namespace.
//
// XRToken publishes each Ark SKU as `volcengine/<ark-id>` and rejects the bare
// Ark id, so this is the same class of fact as the task URL this wrapper already
// rewrites — a wire-dialect difference, not per-account configuration. See
// newapiintegration.XRTokenUpstreamVideoModel for why encoding it here beats
// storing prefixed targets in credentials.model_mapping (short version: the
// mapping SSOT can only express identity, so a prefixed target is both
// unrepresentable and reverted by the next routine apply-accounts).
//
// Everything else about the payload — content parts, resolution/ratio/duration,
// audio and seed fields, first-frame image — is inherited untouched, so an
// upstream Ark schema change still flows through automatically.
//
// info.UpstreamModelName is updated to match what actually goes on the wire.
// That value surfaces as TaskSubmitOutcome.UpstreamModel and lands in the ops
// forward-result evidence, so leaving it as the un-prefixed id would make the
// logs disagree with the request. The BILLING key is unaffected: the handler
// prices on OriginModel, which DispatchVideoSubmit takes from the client-facing
// request model.
func (a *xrTokenTaskAdaptor) BuildRequestBody(c *gin.Context, info *relaycommon.RelayInfo) (io.Reader, error) {
	inner, err := a.TaskAdaptor.BuildRequestBody(c, info)
	if err != nil {
		return nil, err
	}
	if inner == nil {
		return nil, fmt.Errorf("xrtoken video submit: embedded adaptor produced no body")
	}
	raw, err := io.ReadAll(inner)
	if err != nil {
		return nil, fmt.Errorf("xrtoken video submit: read embedded body: %w", err)
	}
	current := gjson.GetBytes(raw, "model").String()
	upstream := newapiintegration.XRTokenUpstreamVideoModel(current)
	if upstream == current {
		// Already namespaced (or empty) — nothing to rewrite. Idempotent so a
		// legacy hand-written prefixed mapping cannot double the prefix.
		return bytes.NewReader(raw), nil
	}
	rewritten, err := sjson.SetBytes(raw, "model", upstream)
	if err != nil {
		return nil, fmt.Errorf("xrtoken video submit: rewrite model to %q: %w", upstream, err)
	}
	if info != nil {
		info.UpstreamModelName = upstream
	}
	return bytes.NewReader(rewritten), nil
}

// sanitizeFetchResponse rewrites the client-facing `model` field of a poll
// response back to the Ark id the caller submitted.
//
// The video poll path hands upstream JSON to the client verbatim (see the
// VideoFetch handler: `body := out.RawResponse` goes straight to the writer).
// That passthrough is deliberate — volcengine/doubao SDK clients should see the
// body shape new-api would return for this channel type — but XRToken's task
// payload carries a `model` field holding the vendor-namespaced id this wrapper
// put on the wire at submit. Without this, one task reports two different model
// names across POST and GET, and the response discloses which reseller served
// the request.
//
// This runs on the ALREADY-BOUNDED body rather than inside FetchTask on purpose:
// videoFetchResponseMaxBytes is enforced by DispatchVideoFetch after FetchTask
// returns, so reading the stream early to rewrite it would bypass that limit and
// pull an unbounded inline-media response into memory.
//
// Returns the input untouched on any parse/rewrite failure: a cosmetic model
// name is never worth failing a poll the client needs for its video URL.
func (a *xrTokenTaskAdaptor) sanitizeFetchResponse(body []byte, _ string) []byte {
	if len(body) == 0 {
		return body
	}
	current := gjson.GetBytes(body, "model")
	if !current.Exists() {
		return body
	}
	clientFacing := newapiintegration.XRTokenClientFacingVideoModel(current.String())
	if clientFacing == current.String() {
		return body
	}
	rewritten, err := sjson.SetBytes(body, "model", clientFacing)
	if err != nil {
		return body
	}
	return rewritten
}
