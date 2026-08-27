package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type recordingProtocolCapabilityProbeScheduler struct {
	calls          chan []int64
	onProbe        func([]int64)
	probeNowResult service.ProtocolProbeRunResult
	probeNowErr    error
}

func (s *recordingProtocolCapabilityProbeScheduler) ProbeAccountProtocolCapabilitiesBatch(_ context.Context, accountIDs []int64) {
	copyIDs := append([]int64(nil), accountIDs...)
	s.calls <- copyIDs
	if s.onProbe != nil {
		s.onProbe(copyIDs)
	}
}

func (s *recordingProtocolCapabilityProbeScheduler) ProbeAccountProtocolCapabilitiesNow(_ context.Context, accountID int64) (service.ProtocolProbeRunResult, error) {
	copyIDs := []int64{accountID}
	s.calls <- copyIDs
	if s.onProbe != nil {
		s.onProbe(copyIDs)
	}
	return s.probeNowResult, s.probeNowErr
}

func governedProtocolProbeAccount(id int64) *service.Account {
	return &service.Account{
		ID:       id,
		Name:     "custom relay",
		Platform: service.PlatformNewAPI,
		Type:     service.AccountTypeAPIKey,
		Status:   service.StatusActive,
		Credentials: map[string]any{
			"api_key":  "secret",
			"base_url": "https://relay.example.test/v1",
			"model_mapping": map[string]any{
				"client-model": "upstream-model",
			},
		},
	}
}

func awaitProtocolProbeCall(t *testing.T, scheduler *recordingProtocolCapabilityProbeScheduler) []int64 {
	t.Helper()
	select {
	case got := <-scheduler.calls:
		return got
	case <-time.After(time.Second):
		t.Fatal("protocol capability probe was not scheduled")
		return nil
	}
}

func TestProtocolCapabilityProbeRequiredForUpdateOnlyOnCapabilityInputs(t *testing.T) {
	priority := 12
	channelType := 7
	tests := []struct {
		name string
		req  UpdateAccountRequest
		want bool
	}{
		{name: "priority only", req: UpdateAccountRequest{Priority: &priority}, want: false},
		{name: "credentials", req: UpdateAccountRequest{Credentials: map[string]any{"base_url": "https://relay.example"}}, want: true},
		{name: "type", req: UpdateAccountRequest{Type: service.AccountTypeUpstream}, want: true},
		{name: "channel type", req: UpdateAccountRequest{ChannelType: &channelType}, want: true},
		{name: "custom base url", req: UpdateAccountRequest{Extra: map[string]any{"custom_base_url": "https://relay.example"}}, want: true},
		{name: "unrelated extra", req: UpdateAccountRequest{Extra: map[string]any{"base_rpm": 10}}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protocolCapabilityProbeRequiredForUpdate(tt.req); got != tt.want {
				t.Fatalf("protocolCapabilityProbeRequiredForUpdate = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestBulkUpdateSchedulesBoundedProtocolProbeBatchForCredentialChanges(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	scheduler := &recordingProtocolCapabilityProbeScheduler{calls: make(chan []int64, 1)}
	handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
	router := gin.New()
	router.PUT("/accounts/bulk", handler.BulkUpdate)

	body := []byte(`{"account_ids":[11,12],"credentials":{"base_url":"https://relay.example/v1"}}`)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/accounts/bulk", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	select {
	case got := <-scheduler.calls:
		if want := []int64{11, 12}; !reflect.DeepEqual(got, want) {
			t.Fatalf("probe account IDs = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("bulk credential update did not schedule protocol capability probes")
	}
}

func TestProbeProtocolsRunsExactAccountAndReturnsRefreshedCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	adminService.getAccountResult = governedProtocolProbeAccount(42)
	lastProbedAt := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	capabilityID := int64(9)
	capability := &service.ProtocolEndpointCapability{
		ID:                 capabilityID,
		CapabilityKey:      "endpoint-capability-key",
		SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses},
		Revision:           7,
		LastProbedAt:       &lastProbedAt,
	}
	scheduler := &recordingProtocolCapabilityProbeScheduler{
		calls: make(chan []int64, 1),
		probeNowResult: service.ProtocolProbeRunResult{
			Outcome:              service.ProtocolProbeRunUpdated,
			Reason:               "positive_evidence",
			Capability:           capability,
			AffectedAccountCount: 3,
		},
		onProbe: func([]int64) {
			adminService.getAccountResult.ProtocolEndpointCapabilityID = &capabilityID
			adminService.getAccountResult.ProtocolEndpointCapability = capability
		},
	}
	handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
	router := gin.New()
	router.POST("/accounts/:id/protocol-probe", handler.ProbeProtocols)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts/42/protocol-probe", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got, want := awaitProtocolProbeCall(t, scheduler), []int64{42}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probe account IDs = %v, want %v", got, want)
	}
	var responseBody struct {
		Data struct {
			Account struct {
				SupportedProtocols []string `json:"supported_protocols"`
			} `json:"account"`
			Capability struct {
				CapabilityKey      string     `json:"capability_key"`
				SupportedProtocols []string   `json:"supported_protocols"`
				Revision           int64      `json:"revision"`
				LastProbedAt       *time.Time `json:"last_probed_at"`
				AffectedCount      int        `json:"affected_account_count"`
			} `json:"capability"`
			Outcome service.ProtocolProbeRunOutcome `json:"outcome"`
			Reason  string                          `json:"reason"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &responseBody); err != nil {
		t.Fatalf("decode response: %v body=%s", err, recorder.Body.String())
	}
	if got, want := responseBody.Data.Account.SupportedProtocols, []string{"responses"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("account supported_protocols = %v, want %v", got, want)
	}
	if got, want := responseBody.Data.Capability.CapabilityKey, "endpoint-capability-key"; got != want {
		t.Fatalf("capability key = %q, want %q", got, want)
	}
	if got, want := responseBody.Data.Capability.SupportedProtocols, []string{"responses"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("capability supported_protocols = %v, want %v", got, want)
	}
	if responseBody.Data.Capability.Revision != 7 || responseBody.Data.Capability.AffectedCount != 3 {
		t.Fatalf("capability metadata = %+v", responseBody.Data.Capability)
	}
	if responseBody.Data.Capability.LastProbedAt == nil || !responseBody.Data.Capability.LastProbedAt.Equal(lastProbedAt) {
		t.Fatalf("last_probed_at = %v, want %v", responseBody.Data.Capability.LastProbedAt, lastProbedAt)
	}
	if responseBody.Data.Outcome != service.ProtocolProbeRunUpdated || responseBody.Data.Reason != "positive_evidence" {
		t.Fatalf("probe result = outcome %q reason %q", responseBody.Data.Outcome, responseBody.Data.Reason)
	}
}

func TestProbeProtocolsReturnsNotApplicableInsteadOfFalseSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	adminService.getAccountResult = &service.Account{
		ID:       43,
		Name:     "image only",
		Platform: service.PlatformNewAPI,
		Type:     service.AccountTypeAPIKey,
		Credentials: map[string]any{
			"api_key":       "secret",
			"base_url":      "https://images.example.test/v1",
			"model_mapping": map[string]any{"image": "dall-e-3"},
		},
	}
	scheduler := &recordingProtocolCapabilityProbeScheduler{calls: make(chan []int64, 1)}
	handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
	router := gin.New()
	router.POST("/accounts/:id/protocol-probe", handler.ProbeProtocols)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts/43/protocol-probe", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"outcome":"not_applicable"`) {
		t.Fatalf("response did not explain that no probe applies: %s", recorder.Body.String())
	}
	select {
	case got := <-scheduler.calls:
		t.Fatalf("not-applicable account unexpectedly ran probe for %v", got)
	default:
	}
}

func TestProbeProtocolsSurfacesSynchronousProbeFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	adminService.getAccountResult = governedProtocolProbeAccount(44)
	scheduler := &recordingProtocolCapabilityProbeScheduler{
		calls:       make(chan []int64, 1),
		probeNowErr: errors.New("probe persistence failed"),
	}
	handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
	router := gin.New()
	router.POST("/accounts/:id/protocol-probe", handler.ProbeProtocols)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts/44/protocol-probe", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got, want := awaitProtocolProbeCall(t, scheduler), []int64{44}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probe account IDs = %v, want %v", got, want)
	}
}

func TestSetSchedulableSchedulesProtocolProbeOnlyWhenEnabling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name          string
		schedulable   bool
		wantScheduled bool
	}{
		{name: "enable", schedulable: true, wantScheduled: true},
		{name: "disable", schedulable: false, wantScheduled: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			adminService := newStubAdminService()
			adminService.setSchedulableResult = governedProtocolProbeAccount(51)
			scheduler := &recordingProtocolCapabilityProbeScheduler{calls: make(chan []int64, 1)}
			handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
			router := gin.New()
			router.POST("/accounts/:id/schedulable", handler.SetSchedulable)

			body := []byte(`{"schedulable":` + strconv.FormatBool(tt.schedulable) + `}`)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/accounts/51/schedulable", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
			}

			if tt.wantScheduled {
				if got, want := awaitProtocolProbeCall(t, scheduler), []int64{51}; !reflect.DeepEqual(got, want) {
					t.Fatalf("probe account IDs = %v, want %v", got, want)
				}
				return
			}
			select {
			case got := <-scheduler.calls:
				t.Fatalf("disabling account scheduled protocol probe for %v", got)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func TestClearErrorSchedulesProtocolProbeAfterSuccessfulRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	adminService := newStubAdminService()
	adminService.clearAccountErrorResult = governedProtocolProbeAccount(61)
	scheduler := &recordingProtocolCapabilityProbeScheduler{calls: make(chan []int64, 1)}
	handler := &AccountHandler{adminService: adminService, protocolProbeScheduler: scheduler}
	router := gin.New()
	router.POST("/accounts/:id/clear-error", handler.ClearError)

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/accounts/61/clear-error", nil)
	router.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if got, want := awaitProtocolProbeCall(t, scheduler), []int64{61}; !reflect.DeepEqual(got, want) {
		t.Fatalf("probe account IDs = %v, want %v", got, want)
	}
}
