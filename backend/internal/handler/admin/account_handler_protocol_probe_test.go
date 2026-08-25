package admin

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type recordingProtocolCapabilityProbeScheduler struct {
	calls chan []int64
}

func (s *recordingProtocolCapabilityProbeScheduler) ProbeAccountProtocolCapabilitiesBatch(_ context.Context, accountIDs []int64) {
	copyIDs := append([]int64(nil), accountIDs...)
	s.calls <- copyIDs
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
