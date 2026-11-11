//go:build unit

package bridge

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	newapiintegration "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/gin-gonic/gin"
)

func TestVolcenginePlanContentsURL(t *testing.T) {
	t.Parallel()
	want := newapiintegration.VolcEngineAgentPlanBaseURL + "/contents/generations/tasks"
	cases := []struct {
		name   string
		base   string
		taskID string
		want   string
	}{
		{"plan root", newapiintegration.VolcEngineAgentPlanBaseURL, "", want},
		{"sentinel key", newapiintegration.VolcEngineAgentPlanBaseKey, "", want},
		{"chat suffix", newapiintegration.VolcEngineAgentPlanBaseURL + "/chat/completions", "", want},
		{"poll", newapiintegration.VolcEngineAgentPlanBaseURL, "cgt-1", want + "/cgt-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := volcenginePlanContentsURL(tc.base, tc.taskID)
			if err != nil {
				t.Fatalf("volcenginePlanContentsURL(%q, %q) error: %v", tc.base, tc.taskID, err)
			}
			if got != tc.want {
				t.Fatalf("volcenginePlanContentsURL(%q, %q) = %q, want %q", tc.base, tc.taskID, got, tc.want)
			}
		})
	}
	if _, err := volcenginePlanContentsURL("   ", ""); err == nil {
		t.Fatal("empty base must fail")
	}
}

func TestTaskAdaptorForChannel_VolcenginePlanSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		channel  int
		base     string
		wantPlan bool
	}{
		{"plan ch45", newapiconstant.ChannelTypeVolcEngine, newapiintegration.VolcEngineAgentPlanBaseURL, true},
		{"sentinel ch45", newapiconstant.ChannelTypeVolcEngine, newapiintegration.VolcEngineAgentPlanBaseKey, true},
		{"paygo ark ch45", newapiconstant.ChannelTypeVolcEngine, "https://ark.cn-beijing.volces.com", false},
		{"plan url on ch54", newapiconstant.ChannelTypeDoubaoVideo, newapiintegration.VolcEngineAgentPlanBaseURL, false},
		{"empty ch45", newapiconstant.ChannelTypeVolcEngine, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := taskAdaptorForChannel(tc.channel, tc.base)
			if adaptor == nil {
				t.Fatalf("taskAdaptorForChannel(%d, %q) = nil", tc.channel, tc.base)
			}
			_, isPlan := adaptor.(*volcenginePlanTaskAdaptor)
			if isPlan != tc.wantPlan {
				t.Fatalf("taskAdaptorForChannel(%d, %q) plan=%v, want %v", tc.channel, tc.base, isPlan, tc.wantPlan)
			}
		})
	}
}

func TestVolcenginePlanTaskAdaptor_DoRequest_HitsPlanContentsPath(t *testing.T) {
	ensureNewAPIDeps()
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/contents/generations/tasks" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cgt-plan-submit-1"}`))
	}))
	defer srv.Close()

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/contents/generations/tasks", nil)
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    newapiconstant.ChannelTypeVolcEngine,
			ChannelBaseUrl: srv.URL,
			ApiKey:         "plan-test-key",
		},
	}
	a := newVolcenginePlanTaskAdaptor()
	a.Init(info)
	resp, err := a.DoRequest(c, info, bytes.NewReader([]byte(`{"model":"doubao-seedance-2.5"}`)))
	if err != nil {
		t.Fatalf("DoRequest error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("DoRequest status = %d body %s; embedded adaptor would request /api/v3/...", resp.StatusCode, body)
	}
	if gotPath != "/contents/generations/tasks" {
		t.Fatalf("submit hit %q, want /contents/generations/tasks", gotPath)
	}
	if gotAuth != "Bearer plan-test-key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
}

func TestVolcenginePlanTaskAdaptor_FetchTask_HitsPlanContentsPath(t *testing.T) {
	ensureNewAPIDeps()
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/contents/generations/tasks/cgt-plan-fetch-1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cgt-plan-fetch-1","status":"succeeded"}`))
	}))
	defer srv.Close()

	a := newVolcenginePlanTaskAdaptor()
	resp, err := a.FetchTask(srv.URL, "plan-test-key", map[string]any{"task_id": "cgt-plan-fetch-1"}, "")
	if err != nil {
		t.Fatalf("FetchTask error: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("FetchTask status = %d, want 200", resp.StatusCode)
	}
	if gotPath != "/contents/generations/tasks/cgt-plan-fetch-1" {
		t.Fatalf("fetch hit %q", gotPath)
	}
	if gotAuth != "Bearer plan-test-key" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	if _, err := a.FetchTask("", "k", map[string]any{"task_id": "x"}, ""); err == nil {
		t.Fatal("empty base must fail")
	}
	if _, err := a.FetchTask(srv.URL, "k", map[string]any{}, ""); err == nil {
		t.Fatal("missing task_id must fail")
	}
}
