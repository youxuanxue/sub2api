//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type feedbackConfigStub struct{ cfg *OpsEmailNotificationConfig }

func (s feedbackConfigStub) GetEmailNotificationConfig(context.Context) (*OpsEmailNotificationConfig, error) {
	return s.cfg, nil
}

func TestRedactFeedbackRemovesCredentialLikeValues(t *testing.T) {
	got := redactFeedback("api_key=sk-secret-1234567890 Authorization: Bearer abc.def cookie=session password=hunter2")
	require.NotContains(t, got, "sk-secret-1234567890")
	require.NotContains(t, got, "abc.def")
	require.NotContains(t, got, "session")
	require.NotContains(t, got, "hunter2")
	require.Contains(t, got, "[REDACTED]")
}

func TestFeedbackValidationRejectsInvalidInputBeforeDatabaseAccess(t *testing.T) {
	svc := NewFeedbackService(nil, nil)
	_, err := svc.Create(context.Background(), 7, "unknown", "subject", "content", "")
	require.ErrorIs(t, err, ErrFeedbackInvalidCategory)

	_, err = svc.Create(context.Background(), 7, "technical", "", "content", "")
	require.ErrorIs(t, err, ErrFeedbackInvalidFields)

	_, err = svc.UpdateStatus(context.Background(), 1, "unknown")
	require.ErrorIs(t, err, ErrFeedbackInvalidStatus)
}

func TestFeedbackNotifyUsesConfiguredFeishuAndDoesNotExposeContentSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sk-secret-1234567890") {
			t.Errorf("webhook payload exposed API key: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"msg":"ok"}`))
	}))
	defer server.Close()

	svc := &FeedbackService{
		cfgProvider: feedbackConfigStub{cfg: &OpsEmailNotificationConfig{
			Feishu: OpsFeishuAlertConfig{Enabled: true, WebhookURL: server.URL},
		}},
		httpClient: &http.Client{Timeout: opsFeishuWebhookTimeout},
	}
	svc.notify(context.Background(), &FeedbackTicket{
		TicketNo: "TK-1", UserID: 7, Category: "api", Subject: "test", Content: "api_key=sk-secret-1234567890",
	}, "created")
}
