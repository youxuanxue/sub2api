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
	"regexp"
	"strconv"
	"strings"
	"time"
)

const opsFeishuWebhookTimeout = 5 * time.Second

// opsDashboardPath is the admin SPA route for the ops dashboard (frontend
// router name "AdminOps"). Appended to a node's public base URL to build the
// alert card's deep-link.
const opsDashboardPath = "/admin/ops"

// opsNodeShortLabelRe extracts the edge id from a node's public host, e.g.
// api-us1.tokenkey.dev -> us1. A bare api.<domain> (prod) has no edge id and
// falls back to "prod".
var opsNodeShortLabelRe = regexp.MustCompile(`^api-([a-z0-9]+)\.`)

// deriveOpsNodeIdentity turns a node's public base URL (config.Server.FrontendURL,
// e.g. https://api-us1.tokenkey.dev) into a short label + ops dashboard deep-link
// for the alert card. TokenKey runs prod + multiple edges, each evaluating its
// own local metrics and posting to the same Feishu group, so without this every
// P0 card is indistinguishable. An empty/unparseable URL degrades to
// ("overall", "") — the label is still shown, the link is omitted.
func deriveOpsNodeIdentity(frontendURL string) (label string, dashboardURL string) {
	raw := strings.TrimSpace(frontendURL)
	if raw == "" {
		return "overall", ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" {
		return "overall", ""
	}
	host := parsed.Hostname()
	switch {
	case opsNodeShortLabelRe.MatchString(host):
		label = opsNodeShortLabelRe.FindStringSubmatch(host)[1]
	case strings.HasPrefix(host, "api."):
		label = "prod"
	default:
		label = host
	}
	dashboardURL = strings.TrimRight(raw, "/") + opsDashboardPath
	return label, dashboardURL
}

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

func (n *opsFeishuNotifier) sendAlert(ctx context.Context, cfg OpsFeishuAlertConfig, frontendURL string, rule *OpsAlertRule, event *OpsAlertEvent) error {
	if n == nil {
		n = newOpsFeishuNotifier()
	}
	payload, err := n.buildPayload(cfg, frontendURL, rule, event)
	if err != nil {
		return err
	}
	return sendFeishuPayload(ctx, n.httpClient, cfg, payload)
}

func (n *opsFeishuNotifier) sendRecovery(ctx context.Context, cfg OpsFeishuAlertConfig, frontendURL string, rule *OpsAlertRule, event *OpsAlertEvent, currentMetricValue *float64) error {
	if n == nil {
		n = newOpsFeishuNotifier()
	}
	payload, err := n.buildRecoveryPayload(cfg, frontendURL, rule, event, currentMetricValue)
	if err != nil {
		return err
	}
	return sendFeishuPayload(ctx, n.httpClient, cfg, payload)
}

// feishuCardPayload builds the interactive-card envelope (header template/title
// + lark_md body) and signs it when a secret is configured. Both the ops alert
// path (buildPayload) and the account-incident path reuse it so the card shape +
// signing live in one place.
func feishuCardPayload(cfg OpsFeishuAlertConfig, now func() time.Time, headerTemplate, headerTitle, markdownBody string) map[string]any {
	if now == nil {
		now = time.Now
	}
	payload := map[string]any{
		"msg_type": "interactive",
		"card": map[string]any{
			"header": map[string]any{
				"template": headerTemplate,
				"title": map[string]any{
					"tag":     "plain_text",
					"content": headerTitle,
				},
			},
			"elements": []map[string]any{
				{
					"tag": "div",
					"text": map[string]any{
						"tag":     "lark_md",
						"content": markdownBody,
					},
				},
			},
		},
	}
	if secret := strings.TrimSpace(cfg.SigningSecret); secret != "" {
		timestamp := strconv.FormatInt(now().Unix(), 10)
		payload["timestamp"] = timestamp
		payload["sign"] = signFeishuWebhook(timestamp, secret)
	}
	return payload
}

// sendFeishuPayload is the shared low-level sender: marshal → POST → interpret
// the Feishu response. Reused by the ops alert path and the account-incident
// path so the HTTP / error-sanitization logic lives in exactly one place.
func sendFeishuPayload(ctx context.Context, client opsFeishuHTTPDoer, cfg OpsFeishuAlertConfig, payload map[string]any) error {
	if client == nil {
		client = &http.Client{Timeout: opsFeishuWebhookTimeout}
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

func (n *opsFeishuNotifier) buildPayload(cfg OpsFeishuAlertConfig, frontendURL string, rule *OpsAlertRule, event *OpsAlertEvent) (map[string]any, error) {
	if rule == nil || event == nil {
		return nil, errors.New("missing alert context")
	}
	nodeLabel, dashboardURL := deriveOpsNodeIdentity(frontendURL)
	text := buildOpsFeishuAlertText(rule, event, nodeLabel, dashboardURL)
	now := time.Now
	if n != nil && n.now != nil {
		now = n.currentTime
	}
	title := "TokenKey " + opsFeishuSeverityTitle(event.Severity, rule.Severity) + " 告警"
	if nodeLabel != "" && nodeLabel != "overall" {
		title = title + " · " + nodeLabel
	}
	return feishuCardPayload(cfg, now, opsFeishuHeaderTemplate(event.Severity, rule.Severity), title, text), nil
}

func (n *opsFeishuNotifier) buildRecoveryPayload(cfg OpsFeishuAlertConfig, frontendURL string, rule *OpsAlertRule, event *OpsAlertEvent, currentMetricValue *float64) (map[string]any, error) {
	if rule == nil || event == nil {
		return nil, errors.New("missing alert context")
	}
	nodeLabel, dashboardURL := deriveOpsNodeIdentity(frontendURL)
	text := buildOpsFeishuRecoveryText(rule, event, nodeLabel, dashboardURL, currentMetricValue)
	now := time.Now
	if n != nil && n.now != nil {
		now = n.currentTime
	}
	title := "TokenKey " + opsFeishuSeverityTitle(event.Severity, rule.Severity) + " 告警已恢复"
	if nodeLabel != "" && nodeLabel != "overall" {
		title = title + " · " + nodeLabel
	}
	return feishuCardPayload(cfg, now, "green", title, text), nil
}

func (n *opsFeishuNotifier) currentTime() time.Time {
	if n != nil && n.now != nil {
		return n.now()
	}
	return time.Now()
}

func buildOpsFeishuAlertText(rule *OpsAlertRule, event *OpsAlertEvent, nodeLabel, dashboardURL string) string {
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
	if strings.TrimSpace(nodeLabel) == "" {
		nodeLabel = "overall"
	}
	// Severity label for the advice line — use the event's severity (set from
	// the rule at fire time), fall back to the rule's, then a neutral default so
	// a P1 rule no longer reads "处理该 P0 rule".
	sev := strings.TrimSpace(event.Severity)
	if sev == "" {
		sev = strings.TrimSpace(rule.Severity)
	}
	if sev == "" {
		sev = "告警"
	}
	// dashboardURL is our own constructed URL (base + opsDashboardPath); render
	// it as a lark_md link. When the node has no frontend_url configured we fall
	// back to plain prose so the card still reads cleanly.
	advice := buildOpsFeishuAdvice(rule.MetricType, sev, dashboardURL)
	// TK (us7 P0 2026-06-13): the top offending model/reason, when the evaluator
	// attached it (error-rate rules). Rendered as its own line right under 范围 so
	// an operator can tell a real fire from client noise (e.g. a hammered
	// access-gated model) without drilling the dashboard.
	topCauseLine := buildOpsFeishuBreakdownLines(rule.MetricType, event.Dimensions)
	return fmt.Sprintf("**节点**：%s\n**规则**：%s\n**指标**：%s %s %s\n**当前值**：%s\n**范围**：%s%s\n**时间**：%s\n\n**建议**：%s",
		escapeFeishuText(nodeLabel),
		escapeFeishuText(strings.TrimSpace(rule.Name)),
		escapeFeishuText(strings.TrimSpace(rule.MetricType)),
		escapeFeishuText(strings.TrimSpace(rule.Operator)),
		escapeFeishuText(thresholdValue),
		escapeFeishuText(metricValue),
		escapeFeishuText(dimensions),
		topCauseLine,
		escapeFeishuText(formatAlertTime(event.FiredAt)),
		advice,
	)
}

func buildOpsFeishuAdvice(metricType, severity, dashboardURL string) string {
	sev := strings.TrimSpace(severity)
	if sev == "" {
		sev = "告警"
	}
	linkPrefix := "打开 Ops Dashboard"
	if strings.TrimSpace(dashboardURL) != "" {
		linkPrefix = fmt.Sprintf("[打开 Ops Dashboard](%s)", dashboardURL)
	}
	switch strings.TrimSpace(metricType) {
	case OpsAlertMetricUserVisibleFailureCount:
		return fmt.Sprintf("%s 按 user/key/model/root 定位真实用户终态失败，优先止损并确认上游/平台根因。", linkPrefix)
	case OpsAlertMetricClientVisibleFailureCount:
		return fmt.Sprintf("%s 按 user/key/root 定位客户侧参数、内容、权限、限额或用法问题，并同步运营跟进客户。", linkPrefix)
	default:
		return fmt.Sprintf("%s 检查账号可用性 / 网关健康，并处理该 %s rule 指向的容量问题。", linkPrefix, sev)
	}
}

func buildOpsFeishuRecoveryText(rule *OpsAlertRule, event *OpsAlertEvent, nodeLabel, dashboardURL string, currentMetricValue *float64) string {
	metricValue := "-"
	thresholdValue := fmt.Sprintf("%.2f", rule.Threshold)
	if event.MetricValue != nil {
		metricValue = fmt.Sprintf("%.2f", *event.MetricValue)
	}
	if event.ThresholdValue != nil {
		thresholdValue = fmt.Sprintf("%.2f", *event.ThresholdValue)
	}
	currentValue := "-"
	if currentMetricValue != nil {
		currentValue = fmt.Sprintf("%.2f", *currentMetricValue)
	}
	dimensions := formatOpsFeishuDimensions(event.Dimensions)
	if dimensions == "" {
		dimensions = "overall"
	}
	if strings.TrimSpace(nodeLabel) == "" {
		nodeLabel = "overall"
	}
	durationText := "未知"
	resolvedAt := time.Time{}
	if event.ResolvedAt != nil {
		resolvedAt = event.ResolvedAt.UTC()
	}
	if !event.FiredAt.IsZero() && !resolvedAt.IsZero() {
		d := resolvedAt.Sub(event.FiredAt.UTC())
		if d < 0 {
			d = 0
		}
		durationText = formatPoolOutageDuration(d)
	}
	resolvedTimeText := "-"
	if !resolvedAt.IsZero() {
		resolvedTimeText = formatAlertTime(resolvedAt)
	}
	note := "指标已回落到阈值以下，告警自动解除。建议确认根因是否已消除，避免复发。"
	if strings.TrimSpace(dashboardURL) != "" {
		note = fmt.Sprintf("[打开 Ops Dashboard](%s) 复核指标与账号可用性。指标已回落到阈值以下，告警自动解除。", dashboardURL)
	}
	topCauseLine := buildOpsFeishuBreakdownLines(rule.MetricType, event.Dimensions)
	return fmt.Sprintf("**节点**：%s\n**规则**：%s\n**指标**：%s %s %s\n**触发值**：%s\n**当前值**：%s\n**范围**：%s%s\n**持续时长**：%s\n**触发时间**：%s\n**恢复时间**：%s\n\n**说明**：%s",
		escapeFeishuText(nodeLabel),
		escapeFeishuText(strings.TrimSpace(rule.Name)),
		escapeFeishuText(strings.TrimSpace(rule.MetricType)),
		escapeFeishuText(strings.TrimSpace(rule.Operator)),
		escapeFeishuText(thresholdValue),
		escapeFeishuText(metricValue),
		escapeFeishuText(currentValue),
		escapeFeishuText(dimensions),
		topCauseLine,
		escapeFeishuText(durationText),
		escapeFeishuText(formatAlertTime(event.FiredAt)),
		escapeFeishuText(resolvedTimeText),
		note,
	)
}

func opsFeishuSeverityTitle(values ...string) string {
	for _, v := range values {
		sev := strings.ToUpper(strings.TrimSpace(v))
		if sev != "" {
			return sev
		}
	}
	return "告警"
}

func opsFeishuHeaderTemplate(values ...string) string {
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), "P1") {
			return "orange"
		}
	}
	return "red"
}

func buildOpsFeishuBreakdownLines(metricType string, dimensions map[string]any) string {
	if strings.TrimSpace(metricType) == OpsAlertMetricUserVisibleFailureCount ||
		strings.TrimSpace(metricType) == OpsAlertMetricClientVisibleFailureCount {
		return buildOpsFeishuUserVisibleFailureLines(dimensions)
	}
	return buildOpsFeishuTopCauseLines(dimensions)
}

// buildOpsFeishuTopCauseLines renders 主因/用户/模型 breakdown lines shared by
// firing and recovery cards from evaluator-stashed event dimensions.
func buildOpsFeishuTopCauseLines(dimensions map[string]any) string {
	lines := ""
	if cause := opsFeishuTopCause(dimensions); cause != "" {
		lines += fmt.Sprintf("\n**主因**：%s", escapeFeishuText(cause))
	}
	if users := opsFeishuTopCauseUsers(dimensions); users != "" {
		lines += fmt.Sprintf("\n**用户**：%s", escapeFeishuText(users))
	}
	if models := opsFeishuTopCauseModels(dimensions); models != "" {
		lines += fmt.Sprintf("\n**模型**：%s", escapeFeishuText(models))
	}
	return lines
}

func buildOpsFeishuUserVisibleFailureLines(dimensions map[string]any) string {
	lines := ""
	if affected := opsFeishuDimensionString(dimensions, "user_visible_affected"); affected != "" {
		lines += fmt.Sprintf("\n**谁受影响**：%s", escapeFeishuText(affected))
	}
	if impact := opsFeishuDimensionString(dimensions, "user_visible_impact"); impact != "" {
		lines += fmt.Sprintf("\n**影响多大**：%s", escapeFeishuText(impact))
	}
	if surface := opsFeishuDimensionString(dimensions, "user_visible_surface"); surface != "" {
		lines += fmt.Sprintf("\n**用户看到什么**：%s", escapeFeishuText(surface))
	}
	if root := opsFeishuDimensionString(dimensions, "user_visible_root"); root != "" {
		lines += fmt.Sprintf("\n**根因在哪**：%s", escapeFeishuText(root))
	}
	return lines
}

func opsFeishuDimensionString(dimensions map[string]any, key string) string {
	if len(dimensions) == 0 {
		return ""
	}
	if v, ok := dimensions[key]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// opsFeishuTopCause extracts the pre-formatted "主因" string the alert evaluator
// stashed on the event dimensions (survives the DB JSON round-trip as a plain
// string). Returns "" when absent.
func opsFeishuTopCause(dimensions map[string]any) string {
	if len(dimensions) == 0 {
		return ""
	}
	if v, ok := dimensions["top_cause"]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// opsFeishuTopCauseUsers extracts the per-user (client) concentration breakdown
// the evaluator stashed for routing-rejection P0s (survives the DB JSON
// round-trip as a plain string). Rendered on its own line under 主因 so the pool
// cause and the client breakdown each read cleanly. Returns "" when absent (edge
// nodes, non-rejection rules).
func opsFeishuTopCauseUsers(dimensions map[string]any) string {
	if len(dimensions) == 0 {
		return ""
	}
	if v, ok := dimensions["top_cause_users"]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// opsFeishuTopCauseModels extracts the top requested-model breakdown for
// routing-capacity P0s. It is separate from 主因 so platform/user attribution and
// model demand stay independently readable.
func opsFeishuTopCauseModels(dimensions map[string]any) string {
	if len(dimensions) == 0 {
		return ""
	}
	if v, ok := dimensions["top_cause_models"]; ok {
		if s, ok := v.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
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
