//go:build unit

package bridge

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestFMGoTaskAdaptor_BuildRequestURL(t *testing.T) {
	t.Parallel()
	want := newapiintegration.FMGoBaseURL + newapiintegration.FMGoVideosPath
	for _, base := range []string{
		newapiintegration.FMGoBaseURL,
		newapiintegration.FMGoBaseURL + "/v1",
		"https://fmgo.top",
		"https://www.fmgo.top",
	} {
		a := newFMGoTaskAdaptor()
		a.baseURL = newapiintegration.NormalizeFMGoBaseURL(base)
		got, err := a.BuildRequestURL(nil)
		if err != nil {
			t.Fatalf("BuildRequestURL(%q): %v", base, err)
		}
		if got != want {
			t.Fatalf("BuildRequestURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestTaskAdaptorForChannel_SelectsFMGo(t *testing.T) {
	t.Parallel()
	got := taskAdaptorForChannel(newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.FMGoBaseURL)
	if _, ok := got.(*fmgoTaskAdaptor); !ok {
		t.Fatalf("ch54+fmgo host must select fmgo adaptor, got %T", got)
	}
	got = taskAdaptorForChannel(newapiconstant.ChannelTypeDoubaoVideo, "https://www.fmgo.top")
	if _, ok := got.(*fmgoTaskAdaptor); !ok {
		t.Fatalf("legacy www.fmgo.top must still select fmgo adaptor, got %T", got)
	}
	got = taskAdaptorForChannel(newapiconstant.ChannelTypeDoubaoVideo, "https://ark.cn-beijing.volces.com")
	if _, ok := got.(*fmgoTaskAdaptor); ok {
		t.Fatal("official Ark host must not select fmgo adaptor")
	}
}

func buildFMGoSubmitBody(t *testing.T, clientModel, mappingJSON string, extra map[string]any) ([]byte, *relaycommon.RelayInfo, error) {
	t.Helper()
	ensureNewAPIDeps()
	payload := map[string]any{
		"model":  clientModel,
		"prompt": "a cat playing piano",
	}
	for key, value := range extra {
		payload[key] = value
	}
	body, err := normalizeVideoSubmitBodyForTaskAdaptor(mustJSON(t, payload))
	if err != nil {
		return nil, nil, err
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
		BaseURL:          newapiintegration.FMGoBaseURL,
		APIKey:           "fmgo-test-key",
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
	adaptor := newFMGoTaskAdaptor()
	adaptor.Init(relayInfo)
	if taskErr := adaptor.ValidateRequestAndSetAction(c, relayInfo); taskErr != nil {
		return nil, relayInfo, fmt.Errorf("validate: %+v", taskErr)
	}
	reader, err := adaptor.BuildRequestBody(c, relayInfo)
	if err != nil {
		return nil, relayInfo, err
	}
	wire, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read wire: %v", err)
	}
	return wire, relayInfo, nil
}

func TestFMGoTaskAdaptor_RewritesOfficialSeedanceSKU(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	wire, info, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution": "720p",
		"duration":   10,
	})
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	want := "feimiao-v2-431-720p-10s"
	if got := gjson.GetBytes(wire, "model").String(); got != want {
		t.Fatalf("wire model = %q, want %q", got, want)
	}
	if gjson.GetBytes(wire, "async").Exists() {
		t.Fatal("/v1/videos body must not set async")
	}
	if gjson.GetBytes(wire, "seconds").String() != "10" {
		t.Fatalf("seconds = %s", gjson.GetBytes(wire, "seconds").Raw)
	}
	if gjson.GetBytes(wire, "resolution").String() != "720p" {
		t.Fatalf("resolution = %q", gjson.GetBytes(wire, "resolution").String())
	}
	if gjson.GetBytes(wire, "prompt").String() != "a cat playing piano" {
		t.Fatalf("prompt = %q", gjson.GetBytes(wire, "prompt").String())
	}
	if info.UpstreamModelName != want {
		t.Fatalf("UpstreamModelName = %q, want %q", info.UpstreamModelName, want)
	}
	if info.OriginModelName != client {
		t.Fatalf("billing key leaked: OriginModelName=%q", info.OriginModelName)
	}
}

func TestFMGoTaskAdaptor_RewritesPastProbeAnchorMapping(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	anchor := "feimiao-v2-431-720p-15s"
	wire, info, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+anchor+`"}`, map[string]any{
		"resolution": "480p",
		"duration":   10,
	})
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	want := "feimiao-v2-431-480p-10s"
	if got := gjson.GetBytes(wire, "model").String(); got != want {
		t.Fatalf("wire model = %q, want %q — probe-anchor mapping must not freeze the SKU", got, want)
	}
	if info.UpstreamModelName != want {
		t.Fatalf("UpstreamModelName = %q, want %q", info.UpstreamModelName, want)
	}
	if info.OriginModelName != client {
		t.Fatalf("billing key leaked: OriginModelName=%q", info.OriginModelName)
	}
}

func TestFMGoTaskAdaptor_DefaultsToMaxResolutionAndDuration(t *testing.T) {
	client := newapiintegration.FMGoSeedanceFastClientID
	wire, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, nil)
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	if got := gjson.GetBytes(wire, "model").String(); got != "feimiao-v2-431-fast-720p-15s" {
		t.Fatalf("default sku = %q", got)
	}
}

func TestFMGoTaskAdaptor_RewritesOfficialSeedance25SKU(t *testing.T) {
	client := newapiintegration.FMGoSeedance25ClientID
	wire, info, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"feimiao-v2.5-720p-15s"}`, map[string]any{
		"resolution": "720p",
		"duration":   30,
	})
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	want := "feimiao-v2.5-720p-30s"
	if got := gjson.GetBytes(wire, "model").String(); got != want {
		t.Fatalf("wire model = %q, want %q", got, want)
	}
	if gjson.GetBytes(wire, "seconds").String() != "30" {
		t.Fatalf("seconds = %s", gjson.GetBytes(wire, "seconds").Raw)
	}
	if info.UpstreamModelName != want {
		t.Fatalf("UpstreamModelName = %q, want %q", info.UpstreamModelName, want)
	}
	if info.OriginModelName != client {
		t.Fatalf("billing key leaked: OriginModelName=%q", info.OriginModelName)
	}
}

func TestFMGoTaskAdaptor_RejectsUnsupportedSeedance25Combo(t *testing.T) {
	client := newapiintegration.FMGoSeedance25ClientID
	_, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution": "720p",
		"duration":   5,
	})
	if err == nil {
		t.Fatal("expected rejection for v2.5 720p/5s")
	}
}

func TestFMGoTaskAdaptor_RejectsUnsupportedResolution(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	_, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution": "1080p",
		"duration":   10,
	})
	if err == nil {
		t.Fatal("expected rejection for 1080p")
	}
}

func TestFMGoTaskAdaptor_RejectsUnsupportedDuration(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	_, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution": "720p",
		"duration":   9,
	})
	if err == nil {
		t.Fatal("expected rejection for 9s")
	}
}

func TestFMGoTaskAdaptor_ReadsDurationSecondsAlias(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	wire, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution":       "720p",
		"duration_seconds": 10,
	})
	if err != nil {
		t.Fatalf("BuildRequestBody: %v", err)
	}
	if got := gjson.GetBytes(wire, "model").String(); got != "feimiao-v2-431-720p-10s" {
		t.Fatalf("duration_seconds=10 must rewrite to feimiao-v2-431-720p-10s, got %q", got)
	}
}

func TestFMGoTaskAdaptor_SanitizeFetchResponse(t *testing.T) {
	t.Parallel()
	adaptor := newFMGoTaskAdaptor()
	got := adaptor.sanitizeFetchResponse([]byte(`{"id":"t1","model":"feimiao-v2-431-720p-10s","status":"succeeded"}`))
	if gjson.GetBytes(got, "model").String() != newapiintegration.FMGoSeedanceClientID {
		t.Fatalf("non-fast sku model = %q", gjson.GetBytes(got, "model").String())
	}
	if gjson.GetBytes(got, "status").String() != "succeeded" || gjson.GetBytes(got, "id").String() != "t1" {
		t.Fatalf("sanitize must only rewrite model, got %s", got)
	}
	got = adaptor.sanitizeFetchResponse([]byte(`{"model":"feimiao-v2-fast-480p-8s"}`))
	if gjson.GetBytes(got, "model").String() != newapiintegration.FMGoSeedanceFastClientID {
		t.Fatalf("fast sku model = %q", gjson.GetBytes(got, "model").String())
	}
	got = adaptor.sanitizeFetchResponse([]byte(`{"model":"feimiao-v2.5-720p-30s"}`))
	if gjson.GetBytes(got, "model").String() != newapiintegration.FMGoSeedance25ClientID {
		t.Fatalf("v2.5 sku model = %q", gjson.GetBytes(got, "model").String())
	}
	for name, body := range map[string]string{
		"no model":       `{"id":"x","status":"succeeded"}`,
		"already client": `{"model":"doubao-seedance-2-0-260128"}`,
		"not json":       `<html>error</html>`,
	} {
		t.Run(name, func(t *testing.T) {
			rewritten := adaptor.sanitizeFetchResponse([]byte(body))
			if string(rewritten) != body {
				t.Fatalf("must leave foreign body alone:\n got: %s\nwant: %s", rewritten, body)
			}
		})
	}
	if got := adaptor.sanitizeFetchResponse(nil); got != nil {
		t.Fatalf("nil body must stay nil, got %q", got)
	}
}

func TestFMGoTaskAdaptor_DoRequest_HitsVideos(t *testing.T) {
	ensureNewAPIDeps()
	var gotPath, gotPrefer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotPrefer = r.Header.Get("Prefer")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"task-fmgo-1"}`))
	}))
	defer srv.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/video/generations", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    newapiconstant.ChannelTypeDoubaoVideo,
			ChannelBaseUrl: srv.URL,
			ApiKey:         "fmgo-key",
		},
		OriginModelName: newapiintegration.FMGoSeedanceClientID,
	}
	a := newFMGoTaskAdaptor()
	a.baseURL = srv.URL
	a.TaskAdaptor.Init(info)
	resp, err := a.DoRequest(c, info, bytes.NewReader([]byte(`{"model":"feimiao-v2-431-720p-15s","prompt":"probe"}`)))
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	_ = resp.Body.Close()
	if gotPath != newapiintegration.FMGoVideosPath {
		t.Fatalf("path = %q, want %s", gotPath, newapiintegration.FMGoVideosPath)
	}
	if gotPrefer != "" {
		t.Fatalf("videos dialect must not send Prefer, got %q", gotPrefer)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("videos create is already 200, got %d", resp.StatusCode)
	}
}

func TestFMGoTaskAdaptor_FetchTask_HitsVideosPath(t *testing.T) {
	t.Parallel()
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"task-1","status":"completed","result":{"url":"https://static.fmgo.top/v.mp4"}}`))
	}))
	defer srv.Close()
	a := newFMGoTaskAdaptor()
	resp, err := a.FetchTask(srv.URL, "k", map[string]any{"task_id": "task-1"}, "")
	if err != nil {
		t.Fatalf("FetchTask: %v", err)
	}
	_ = resp.Body.Close()
	if gotPath != "/v1/videos/task-1" {
		t.Fatalf("fetch path = %q, want /v1/videos/task-1", gotPath)
	}
}

func TestFMGoTaskAdaptor_ParseTaskResult_Completed(t *testing.T) {
	t.Parallel()
	a := newFMGoTaskAdaptor()
	info, err := a.ParseTaskResult([]byte(`{"id":"task-1","status":"completed","result":{"url":"https://static.fmgo.top/v.mp4"}}`))
	if err != nil {
		t.Fatalf("ParseTaskResult: %v", err)
	}
	if info.Url != "https://static.fmgo.top/v.mp4" {
		t.Fatalf("completed url = %q status=%q", info.Url, info.Status)
	}
	if string(info.Status) != "SUCCESS" {
		t.Fatalf("completed status = %q, want SUCCESS", info.Status)
	}
}

func TestFMGoTaskAdaptor_RejectsLegacy6sOnVideosFamily(t *testing.T) {
	client := newapiintegration.FMGoSeedanceClientID
	_, _, err := buildFMGoSubmitBody(t, client, `{"`+client+`":"`+client+`"}`, map[string]any{
		"resolution": "720p",
		"duration":   6,
	})
	if err == nil {
		t.Fatal("431 family must reject 6s")
	}
}

func TestFMGoTaskAdaptor_ParseTaskResult_VideosResultURL(t *testing.T) {
	t.Parallel()
	a := newFMGoTaskAdaptor()
	info, err := a.ParseTaskResult([]byte(`{"id":"task-1","status":"completed","result_url":"https://static.fmgo.top/v.mp4"}`))
	if err != nil {
		t.Fatalf("ParseTaskResult: %v", err)
	}
	if info.Url != "https://static.fmgo.top/v.mp4" {
		t.Fatalf("result_url = %q", info.Url)
	}
}
