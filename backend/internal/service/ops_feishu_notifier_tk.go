package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const opsFeishuWebhookTimeout = 5 * time.Second

type opsFeishuHTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

type opsFeishuNotifier struct {
	httpClient opsFeishuHTTPDoer
	now        func() time.Time
}

func newOpsFeishuNotifier() *opsFeishuNotifier {
	return &opsFeishuNotifier{
		httpClient: &http.Client{Timeout: opsFeishuWebhookTimeout},
		now:        time.Now,
	}
}

func (n *opsFeishuNotifier) sendAlert(ctx context.Context, cfg OpsFeishuAlertConfig, rule *OpsAlertRule, event *OpsAlertEvent) error {
	if n == nil {
		n = newOpsFeishuNotifier()
	}
	client := n.httpClient
	if client == nil {
		client = &http.Client{Timeout: opsFeishuWebhookTimeout}
	}

	payload, err := n.buildPayload(cfg, rule, event)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode feishu payload: %w", err)
	}

	endpoint := strings.TrimSpace(cfg.WebhookURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create feishu request: %s", sanitizeFeishuWebhookError(err, endpoint))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send feishu request: %s", sanitizeFeishuWebhookError(err, endpoint))
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu webhook returned http_status=%d", resp.StatusCode)
	}
	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if len(respBody) > 0 && json.Unmarshal(respBody, &result) == nil && result.Code != 0 {
		return fmt.Errorf("feishu webhook returned code=%d", result.Code)
	}
	return nil
}

func (n *opsFeishuNotifier) buildPayload(cfg OpsFeishuAlertConfig, rule *OpsAlertRule, event *OpsAlertEvent) (map[string]any, error) {
	if rule == nil || event == nil {
		return nil, errors.New("missing alert context")
	}
	text := buildOpsFeishuAlertText(rule, event)
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": "red",
				"title": map[string]any{
					"tag":     "plain_text",
					"content": "TokenKey P0 告警",
				},
			},
			"elements": []map[string]any{
				{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": text,
					},
				},
			},
		},
	}
	if secret := strings.TrimSpace(cfg.SigningSecret); secret != "" {
		timestamp := strconv.FormatInt(n.currentTime().Unix(), 10)
		payload["timestamp"] = timestamp
		payload["sign"] = signFeishuWebhook(timestamp, secret)
	}
	return payload, nil
}

func (n *opsFeishuNotifier) currentTime() time.Time {
	if n != nil && n.now != nil {
		return n.now()
	}
	return time.Now()
}

func buildOpsFeishuAlertText(rule *OpsAlertRule, event *OpsAlertEvent) string {
	metricValue := "-"
	thresholdValue := fmt.Sprintf("%.2f", rule.Threshold)
	if event.MetricValue != nil {
		metricValue = fmt.Sprintf("%.2f", *event.MetricValue)
	}
	if event.ThresholdValue != nil {
		thresholdValue = fmt.Sprintf("%.2f", *event.ThresholdValue)
	}
	dimensions := formatOpsFeishuDimensions(event.Dimensions)
	if dimensions == "" {
		dimensions = "overall"
	}
	return fmt.Sprintf("**规则**：%s\n**指标**：%s %s %s\n**当前值**：%s\n**范围**：%s\n**时间**：%s\n\n**建议**：打开 Ops Dashboard 检查账号可用性 / 网关健康，并处理该 P0 rule 指向的容量问题。",
		escapeFeishuText(strings.TrimSpace(rule.Name)),
		escapeFeishuText(strings.TrimSpace(rule.MetricType)),
		escapeFeishuText(strings.TrimSpace(rule.Operator)),
		escapeFeishuText(thresholdValue),
		escapeFeishuText(metricValue),
		escapeFeishuText(dimensions),
		escapeFeishuText(event.FiredAt.Format(time.RFC3339)),
	)
}

func formatOpsFeishuDimensions(dimensions map[string]any) string {
	if len(dimensions) == 0 {
		return ""
	}
	parts := make([]string, 0, 2)
	if v, ok := dimensions["platform"]; ok {
		parts = append(parts, fmt.Sprintf("platform=%v", v))
	}
	if v, ok := dimensions["group_id"]; ok {
		parts = append(parts, fmt.Sprintf("group_id=%v", v))
	}
	return strings.Join(parts, " ")
}

func signFeishuWebhook(timestamp string, signingSecret string) string {
	stringToSign := timestamp + "\n" + signingSecret
	mac := hmac.New(sha256.New, []byte(stringToSign))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func sanitizeFeishuWebhook(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "<redacted-feishu-webhook>"
	}
	return parsed.Scheme + "://" + parsed.Host + "/<redacted>"
}

func sanitizeFeishuWebhookError(err error, webhookURL string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.TrimSpace(webhookURL) != "" {
		msg = strings.ReplaceAll(msg, webhookURL, sanitizeFeishuWebhook(webhookURL))
	}
	if parsed, parseErr := url.Parse(strings.TrimSpace(webhookURL)); parseErr == nil && parsed.Host != "" {
		if parsed.Path != "" {
			msg = strings.ReplaceAll(msg, parsed.Path, "/<redacted>")
		}
		if parsed.RawQuery != "" {
			msg = strings.ReplaceAll(msg, parsed.RawQuery, "")
		}
	}
	return msg
}

func escapeFeishuText(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(s)
}
