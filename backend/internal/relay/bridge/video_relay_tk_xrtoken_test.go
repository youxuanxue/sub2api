//go:build unit

package bridge

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	newapihelper "github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/types"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// fakeXRTokenUpstream answers ONLY XRToken's ARK-compatible paths
//
//	POST /v1/contents/generations/tasks
//	GET  /v1/contents/generations/tasks/:id
//
// and 404s everything else — crucially including official Ark's
// /api/v3/contents/generations/tasks. That asymmetry is the whole point: it
// mirrors the live behavior verified against api.xrtoken.net, so a regression
// that drops the wrapper (falling back to the upstream doubao adaptor, which
// hardcodes /api/v3) fails here instead of only failing in production.
type fakeXRTokenUpstream struct {
	lastFetchPath  string
	lastAuthHeader string
	lastSubmitBody []byte
	upstreamTaskID string
}

func (f *fakeXRTokenUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.lastAuthHeader = r.Header.Get("Authorization")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/contents/generations/tasks":
		f.lastSubmitBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `"}`))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/contents/generations/tasks/"):
		f.lastFetchPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"` + f.upstreamTaskID + `","status":"succeeded","content":{"video_url":"https://cdn.example/v.mp4"}}`))
	default:
		// Official Ark shape (/api/v3/...) lands here, exactly as on the real host.
		w.WriteHeader(http.StatusNotFound)
	}
}

// TestXRTokenTaskAdaptor_BuildRequestURL asserts the submit path swap and the
// `/v1` de-duplication. XRToken's own SDK docs hand out
// "https://api.xrtoken.net/v1" as the base, so an admin pasting that verbatim
// must NOT produce /v1/v1/contents/... — that was the one way this override
// could silently 404.
func TestXRTokenTaskAdaptor_BuildRequestURL(t *testing.T) {
	t.Parallel()
	const want = newapiintegration.XRTokenBaseURL + "/v1/contents/generations/tasks"
	for _, base := range []string{
		newapiintegration.XRTokenBaseURL,
		newapiintegration.XRTokenBaseURL + "/",
		newapiintegration.XRTokenBaseURL + "/v1",
		newapiintegration.XRTokenBaseURL + "/v1/",
	} {
		a := newXRTokenTaskAdaptor()
		a.baseURL = newapiintegration.NormalizeXRTokenBaseURL(base)
		got, err := a.BuildRequestURL(nil)
		if err != nil {
			t.Fatalf("BuildRequestURL(base=%q) error: %v", base, err)
		}
		if got != want {
			t.Fatalf("BuildRequestURL(base=%q) = %q, want %q", base, got, want)
		}
	}
}

// TestXRTokenTaskAdaptor_BuildRequestURL_EmptyBase guards against emitting a
// relative URL ("/v1/contents/...") when base_url was never resolved: the HTTP
// client would fail with an opaque error instead of naming the cause.
func TestXRTokenTaskAdaptor_BuildRequestURL_EmptyBase(t *testing.T) {
	t.Parallel()
	a := newXRTokenTaskAdaptor()
	if _, err := a.BuildRequestURL(nil); err == nil {
		t.Fatal("expected error for empty base_url, got nil")
	}
}

// TestXRTokenTaskAdaptor_FetchTask_HitsV1Path drives the real poll path over
// real HTTP against an upstream that serves ONLY XRToken's shape. The fake
// 404s /api/v3/..., so this fails if FetchTask ever reverts to the inherited
// implementation.
//
// FetchTask takes baseUrl as an argument (DispatchVideoFetch re-resolves it
// from the persisted task registry, since a poll may run in a different
// process than the submit), so it can be pointed at httptest directly.
func TestXRTokenTaskAdaptor_FetchTask_HitsV1Path(t *testing.T) {
	upstream := &fakeXRTokenUpstream{upstreamTaskID: "cgt-xr-fetch-888"}
	srv := httptest.NewServer(upstream)
	defer srv.Close()

	// FetchTask builds its own HTTP client from new-api's global pool.
	// DispatchVideoFetch calls ensureNewAPIDeps() before reaching the adaptor;
	// a test driving FetchTask directly must do the same or the pool is nil.
	ensureNewAPIDeps()

	a := newXRTokenTaskAdaptor()
	resp, err := a.FetchTask(srv.URL, "tr-test-key", map[string]any{
		"task_id": "cgt-xr-fetch-888",
	}, "")
	if err != nil {
		t.Fatalf("FetchTask error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("FetchTask status = %d, want 200 (wrong path would 404 here)", resp.StatusCode)
	}
	if upstream.lastFetchPath != "/v1/contents/generations/tasks/cgt-xr-fetch-888" {
		t.Fatalf("fetch hit %q, want /v1/contents/generations/tasks/<id>", upstream.lastFetchPath)
	}
	if upstream.lastAuthHeader != "Bearer tr-test-key" {
		t.Fatalf("auth header = %q, want Bearer tr-test-key", upstream.lastAuthHeader)
	}

	// The inherited parser must still understand the (byte-identical) Ark
	// response body — that is the premise of wrapping instead of forking.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	info, err := a.ParseTaskResult(body)
	if err != nil {
		t.Fatalf("inherited ParseTaskResult failed on XRToken response: %v", err)
	}
	if info == nil || info.Status == "" {
		t.Fatalf("inherited ParseTaskResult returned no status: %+v", info)
	}
}

// TestXRTokenTaskAdaptor_FetchTask_Rejects guards the two fast-fail inputs.
func TestXRTokenTaskAdaptor_FetchTask_Rejects(t *testing.T) {
	t.Parallel()
	a := newXRTokenTaskAdaptor()
	if _, err := a.FetchTask(newapiintegration.XRTokenBaseURL, "k", map[string]any{}, ""); err == nil {
		t.Fatal("expected error for missing task_id, got nil")
	}
	if _, err := a.FetchTask("", "k", map[string]any{"task_id": "x"}, ""); err == nil {
		t.Fatal("expected error for empty base_url, got nil")
	}
}

// TestTaskAdaptorForChannel_XRTokenSelection pins the dispatch rule: the
// wrapper is selected ONLY for ch54 + the XRToken sentinel host. Every other
// combination must keep returning the unmodified upstream adaptor, so this
// override cannot leak onto official Ark accounts that share the same
// task-adaptor registration.
func TestTaskAdaptorForChannel_XRTokenSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		channelType int
		baseURL     string
		wantXR      bool
	}{
		{"xrtoken ch54 root", newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.XRTokenBaseURL, true},
		{"xrtoken ch54 with /v1", newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.XRTokenBaseURL + "/v1", true},
		{"official ark ch54", newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com", false},
		{"xrtoken host on ch45", newapiconstant.ChannelTypeVolcEngine, newapiintegration.XRTokenBaseURL, false},
		{"empty base ch54", newapiconstant.ChannelTypeDoubaoVideo, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := taskAdaptorForChannel(tc.channelType, tc.baseURL)
			if adaptor == nil {
				t.Fatalf("taskAdaptorForChannel(%d, %q) = nil", tc.channelType, tc.baseURL)
			}
			_, isXR := adaptor.(*xrTokenTaskAdaptor)
			if isXR != tc.wantXR {
				t.Fatalf("taskAdaptorForChannel(%d, %q) xrtoken=%v, want %v",
					tc.channelType, tc.baseURL, isXR, tc.wantXR)
			}
		})
	}
}

// TestDispatchVideoSubmit_AppliesModelMapping is the regression test for the
// defect this file's first revision shipped: video was the ONLY bridge relay
// that never called helper.ModelMappedHelper (text/responses/embedding/image
// all do), so credentials.model_mapping was ignored and the client-facing model
// went upstream verbatim. Any account whose upstream names its SKUs differently
// was therefore unreachable — XRToken serves Ark Seedance as
// `volcengine/doubao-seedance-*` and rejects the bare Ark id.
//
// The two assertions encode the invariants that make the fix safe:
//
//  1. the UPSTREAM body carries the mapped name, and
//  2. OriginModel — the billing key the handler prices on — stays the
//     client-facing Ark id. Fixing this by rewriting the request body instead
//     would satisfy (1) and silently break (2), billing $0 for a model absent
//     from the pricing overlay.
func TestDispatchVideoSubmit_AppliesModelMapping(t *testing.T) {
	ensureNewAPIDeps()

	// The upstream here answers official Ark's path, NOT XRToken's: the
	// XRToken wrapper is selected by the sentinel host (api.xrtoken.net), which
	// an httptest server can never be, so this exercises the plain doubao
	// adaptor. That is the right scope — the mapping defect lived in
	// DispatchVideoSubmit and affected EVERY newapi video account, not just
	// XRToken. The XRToken model names below document the motivating case; the
	// URL-swap half of the story is covered by the wrapper tests above.
	var gotUpstreamModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/contents/generations/tasks" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		gotUpstreamModel = gjson.GetBytes(raw, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cgt-xr-map-1"}`))
	}))
	defer srv.Close()

	const clientModel = "doubao-seedance-2-5-260628"
	const upstreamModel = "volcengine/doubao-seedance-2-5-260628"

	body := mustJSON(t, map[string]any{"model": clientModel, "prompt": "a cat"})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	out, apiErr := DispatchVideoSubmit(context.Background(), c, ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeDoubaoVideo,
		BaseURL:          srv.URL,
		APIKey:           "tr-test-key",
		ModelMappingJSON: `{"` + clientModel + `":"` + upstreamModel + `"}`,
	}, "vt_map_1", body)
	if apiErr != nil {
		t.Fatalf("DispatchVideoSubmit error: %v", apiErr)
	}
	if gotUpstreamModel != upstreamModel {
		t.Fatalf("upstream received model %q, want the MAPPED name %q — model_mapping was not applied",
			gotUpstreamModel, upstreamModel)
	}
	if out.OriginModel != clientModel {
		t.Fatalf("OriginModel (billing key) = %q, want the client-facing id %q — "+
			"billing must not drift to the upstream name", out.OriginModel, clientModel)
	}
	if out.UpstreamModel != upstreamModel {
		t.Fatalf("UpstreamModel = %q, want %q", out.UpstreamModel, upstreamModel)
	}
}

// TestDispatchVideoSubmit_IdentityMappingIsNoOp pins the compatibility half of
// the fix. Existing Ark accounts are provisioned with identity whitelists
// (tk_033 / tk_056 write `X: X` to gate which SKUs a pool serves), and adding
// ModelMappedHelper to this path must not disturb them: the helper's cycle
// check resolves `X: X` to IsModelMapped=false, leaving the upstream name
// untouched. Without this test the fix could regress live Seedance serving on
// the official Ark pool.
func TestDispatchVideoSubmit_IdentityMappingIsNoOp(t *testing.T) {
	ensureNewAPIDeps()

	const model = "doubao-seedance-2-0-260128"
	var gotUpstreamModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/contents/generations/tasks" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		gotUpstreamModel = gjson.GetBytes(raw, "model").String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cgt-ark-identity-1"}`))
	}))
	defer srv.Close()

	body := mustJSON(t, map[string]any{"model": model, "prompt": "a cat"})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	out, apiErr := DispatchVideoSubmit(context.Background(), c, ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeVolcEngine,
		BaseURL:          srv.URL,
		APIKey:           "ark-test-key",
		ModelMappingJSON: `{"` + model + `":"` + model + `"}`,
	}, "vt_identity_1", body)
	if apiErr != nil {
		t.Fatalf("DispatchVideoSubmit error: %v", apiErr)
	}
	if gotUpstreamModel != model {
		t.Fatalf("upstream received model %q, want %q unchanged", gotUpstreamModel, model)
	}
	if out.OriginModel != model || out.UpstreamModel != model {
		t.Fatalf("identity mapping must leave both names as %q, got origin=%q upstream=%q",
			model, out.OriginModel, out.UpstreamModel)
	}
}

// buildXRTokenSubmitBody drives the wrapper through the exact same sequence
// DispatchVideoSubmit uses (context keys → RelayInfo → InitChannelMeta →
// model mapping → Validate → BuildRequestBody) and returns the bytes that would
// go on the wire plus the resolved RelayInfo.
//
// The wrapper is selected in production by the sentinel base_url, which an
// httptest server can never carry, so a full DispatchVideoSubmit round-trip
// cannot exercise this override. Driving the documented sequence directly is the
// closest faithful harness: every step below is the same call, in the same
// order, as the dispatcher.
func buildXRTokenSubmitBody(t *testing.T, clientModel, mappingJSON string) ([]byte, *relaycommon.RelayInfo) {
	t.Helper()
	ensureNewAPIDeps()

	// Mirror DispatchVideoSubmit's own first step: the public resolution/audio
	// fields are folded into adaptor metadata before the adaptor ever sees the
	// body. Skipping it here would exercise a body shape production never
	// produces (and would drop `resolution` on the floor).
	body, err := normalizeVideoSubmitBodyForTaskAdaptor(mustJSON(t, map[string]any{
		"model":      clientModel,
		"prompt":     "a cat playing piano",
		"resolution": "720p",
	}))
	if err != nil {
		t.Fatalf("normalizeVideoSubmitBodyForTaskAdaptor: %v", err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if err := installBodyStorage(c, body); err != nil {
		t.Fatalf("installBodyStorage: %v", err)
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))

	PopulateContextKeys(c, ChannelContextInput{
		ChannelType:      newapiconstant.ChannelTypeDoubaoVideo,
		BaseURL:          newapiintegration.XRTokenBaseURL,
		APIKey:           "tr-test-key",
		ModelMappingJSON: mappingJSON,
	})
	SetOriginalModel(c, clientModel)

	relayInfo, err := relaycommon.GenRelayInfo(c, types.RelayFormatTask, nil, nil)
	if err != nil {
		t.Fatalf("GenRelayInfo: %v", err)
	}
	relayInfo.OriginModelName = clientModel
	relayInfo.RelayMode = relayconstant.RelayModeVideoSubmit
	relayInfo.InitChannelMeta(c)
	relayInfo.UpstreamModelName = clientModel
	if err := newapihelper.ModelMappedHelper(c, relayInfo, nil); err != nil {
		t.Fatalf("ModelMappedHelper: %v", err)
	}
	relayInfo.PublicTaskID = "vt_xr_unit"

	adaptor := newXRTokenTaskAdaptor()
	adaptor.Init(relayInfo)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, relayInfo); taskErr != nil {
		t.Fatalf("ValidateRequestAndSetAction: %+v", taskErr)
	}
	reader, err := adaptor.BuildRequestBody(c, relayInfo)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	wire, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read wire body: %v", err)
	}
	return wire, relayInfo
}

// TestXRTokenTaskAdaptor_BuildRequestBody_PrefixesModel is the regression for
// the vendor-prefix design decision.
//
// The account carries an IDENTITY model_mapping — the only shape TokenKey's
// mapping SSOT can express (every newapi branch of accountModelMappingForAccount
// returns identityModelMapping) and therefore the only shape that survives a
// routine `apply-accounts`. XRToken still requires the vendor-namespaced id on
// the wire, so the adaptor supplies the prefix itself.
//
// Both halves are asserted together because getting either wrong is a
// production defect: without the prefix XRToken rejects every Seedance request,
// and if the prefix leaked into the billing key the overlay lookup would miss
// and the task would bill $0.
func TestXRTokenTaskAdaptor_BuildRequestBody_PrefixesModel(t *testing.T) {
	const clientModel = "doubao-seedance-2-5-260628"
	const wantUpstream = "volcengine/doubao-seedance-2-5-260628"

	wire, info := buildXRTokenSubmitBody(t, clientModel, `{"`+clientModel+`":"`+clientModel+`"}`)

	if got := gjson.GetBytes(wire, "model").String(); got != wantUpstream {
		t.Fatalf("wire model = %q, want %q — XRToken rejects the bare Ark id", got, wantUpstream)
	}
	if info.UpstreamModelName != wantUpstream {
		t.Fatalf("UpstreamModelName = %q, want %q (ops evidence must match the wire)",
			info.UpstreamModelName, wantUpstream)
	}
	if info.OriginModelName != clientModel {
		t.Fatalf("OriginModelName (billing key) = %q, want the bare Ark id %q — "+
			"the vendor prefix must never reach the pricing key", info.OriginModelName, clientModel)
	}
	// The inherited payload fields must survive the rewrite untouched.
	if got := gjson.GetBytes(wire, "resolution").String(); got != "720p" {
		t.Fatalf("resolution = %q, want 720p — the rewrite must only touch `model`", got)
	}
}

// TestXRTokenTaskAdaptor_BuildRequestBody_AlreadyPrefixedIsNotDoubled covers the
// compatibility case: an operator (or a legacy hand-written mapping predating
// this fix) may still map straight to the namespaced id. The rewrite must be a
// no-op there rather than producing `volcengine/volcengine/...`.
func TestXRTokenTaskAdaptor_BuildRequestBody_AlreadyPrefixedIsNotDoubled(t *testing.T) {
	const clientModel = "doubao-seedance-2-0-260128"
	const wantUpstream = "volcengine/doubao-seedance-2-0-260128"

	wire, info := buildXRTokenSubmitBody(t, clientModel, `{"`+clientModel+`":"`+wantUpstream+`"}`)

	if got := gjson.GetBytes(wire, "model").String(); got != wantUpstream {
		t.Fatalf("wire model = %q, want %q with no doubled prefix", got, wantUpstream)
	}
	if info.OriginModelName != clientModel {
		t.Fatalf("OriginModelName = %q, want %q", info.OriginModelName, clientModel)
	}
}
