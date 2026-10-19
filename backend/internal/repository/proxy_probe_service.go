package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func NewProxyExitInfoProber(cfg *config.Config) service.ProxyExitInfoProber {
	insecure := false
	allowPrivate := false
	validateResolvedIP := true
	maxResponseBytes := defaultProxyProbeResponseMaxBytes
	if cfg != nil {
		insecure = cfg.Security.ProxyProbe.InsecureSkipVerify
		allowPrivate = cfg.Security.URLAllowlist.AllowPrivateHosts
		validateResolvedIP = cfg.Security.URLAllowlist.Enabled
		if cfg.Gateway.ProxyProbeResponseReadMaxBytes > 0 {
			maxResponseBytes = cfg.Gateway.ProxyProbeResponseReadMaxBytes
		}
	}
	if insecure {
		log.Printf("[ProxyProbe] Warning: insecure_skip_verify is not allowed and will cause probe failure.")
	}

	return &proxyProbeService{
		insecureSkipVerify:  insecure,
		allowPrivateHosts:   allowPrivate,
		validateResolvedIP:  validateResolvedIP,
		maxResponseBytes:    maxResponseBytes,
		configuredProbeURLs: resolveProxyProbeTargets(cfg),
	}
}

const (
	defaultProxyProbeTimeout          = 10 * time.Second
	defaultProxyProbeResponseMaxBytes = int64(1024 * 1024)
)

type probeTarget struct {
	url    string
	parser string
}

// probeURLsDefault is the overseas builtin chain (global / Tokyo prod).
// Some AI-only proxies allowlist specific hosts, so keep multiple fallbacks.
var probeURLsDefault = []probeTarget{
	{"http://ip-api.com/json/?lang=zh-CN", "ip-api"},
	{"http://api64.ipify.org?format=json", "ipify"},
}

// probeURLsDomestic is the China-first builtin chain for cn-prod.
// Order: dedicated IP DBs first (stable), then fast opportunistic, then overseas.
// Evidence 2026-10-19 via cn-prod SOCKS: ipip ~80–200ms, ip9 http ~100ms,
// qq-ip2city ~70ms, api64.ipify ~2–3s; ip-api often 4–8s or SOCKS-fail.
var probeURLsDomestic = []probeTarget{
	{"https://myip.ipip.net/json", "ipip-json"},
	{"http://ip9.com.cn/get", "ip9"},
	{"https://r.inews.qq.com/api/ip2city", "qq-ip2city"},
	{"https://api64.ipify.org?format=json", "ipify"},
}

type proxyProbeService struct {
	insecureSkipVerify  bool
	allowPrivateHosts   bool
	validateResolvedIP  bool
	maxResponseBytes    int64
	configuredProbeURLs []probeTarget
}

func resolveProxyProbeTargets(cfg *config.Config) []probeTarget {
	if cfg != nil && len(cfg.Security.ProxyProbe.URLs) > 0 {
		out := make([]probeTarget, 0, len(cfg.Security.ProxyProbe.URLs))
		for _, u := range cfg.Security.ProxyProbe.URLs {
			out = append(out, probeTarget{url: u.URL, parser: u.Parser})
		}
		return out
	}

	if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.Security.ProxyProbe.Preset), "domestic") {
		return append([]probeTarget(nil), probeURLsDomestic...)
	}
	return append([]probeTarget(nil), probeURLsDefault...)
}

func (s *proxyProbeService) ProbeProxy(ctx context.Context, proxyURL string) (*service.ProxyExitInfo, int64, error) {
	client, err := httpclient.GetClient(httpclient.Options{
		ProxyURL:           proxyURL,
		Timeout:            defaultProxyProbeTimeout,
		InsecureSkipVerify: s.insecureSkipVerify,
		ValidateResolvedIP: s.validateResolvedIP,
		AllowPrivateHosts:  s.allowPrivateHosts,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create proxy client: %w", err)
	}

	var lastErr error
	for _, probe := range s.configuredProbeURLs {
		exitInfo, latencyMs, err := s.probeWithURL(ctx, client, probe.url, probe.parser)
		if err == nil {
			return exitInfo, latencyMs, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		return nil, 0, fmt.Errorf("no proxy probe URLs configured")
	}
	return nil, 0, fmt.Errorf("all probe URLs failed, last error: %w", lastErr)
}

func (s *proxyProbeService) probeWithURL(ctx context.Context, client *http.Client, url string, parser string) (*service.ProxyExitInfo, int64, error) {
	startTime := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to create request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("proxy connection failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	latencyMs := time.Since(startTime).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		return nil, latencyMs, fmt.Errorf("request failed with status: %d", resp.StatusCode)
	}

	maxResponseBytes := s.maxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = defaultProxyProbeResponseMaxBytes
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, latencyMs, fmt.Errorf("failed to read response: %w", err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, latencyMs, fmt.Errorf("proxy probe response exceeds limit: %d", maxResponseBytes)
	}

	switch parser {
	case "ip-api":
		return s.parseIPAPI(body, latencyMs)
	case "ipify":
		return s.parseIPify(body, latencyMs)
	case "chatgpt-trace":
		return s.parseChatGPTTrace(body, latencyMs)
	case "ipip-json":
		return s.parseIPIPJSON(body, latencyMs)
	case "ip9":
		return s.parseIP9(body, latencyMs)
	case "qq-ip2city":
		return s.parseQQIP2City(body, latencyMs)
	default:
		return nil, latencyMs, fmt.Errorf("unknown parser: %s", parser)
	}
}

func (s *proxyProbeService) parseIPAPI(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var ipInfo struct {
		Status      string `json:"status"`
		Message     string `json:"message"`
		Query       string `json:"query"`
		City        string `json:"city"`
		Region      string `json:"region"`
		RegionName  string `json:"regionName"`
		Country     string `json:"country"`
		CountryCode string `json:"countryCode"`
	}

	if err := json.Unmarshal(body, &ipInfo); err != nil {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, latencyMs, fmt.Errorf("failed to parse response: %w (body: %s)", err, preview)
	}
	if strings.ToLower(ipInfo.Status) != "success" {
		if ipInfo.Message == "" {
			ipInfo.Message = "ip-api request failed"
		}
		return nil, latencyMs, fmt.Errorf("ip-api request failed: %s", ipInfo.Message)
	}

	region := ipInfo.RegionName
	if region == "" {
		region = ipInfo.Region
	}
	return &service.ProxyExitInfo{
		IP:          ipInfo.Query,
		City:        ipInfo.City,
		Region:      region,
		Country:     ipInfo.Country,
		CountryCode: ipInfo.CountryCode,
	}, latencyMs, nil
}

func (s *proxyProbeService) parseIPify(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var result struct {
		IP string `json:"ip"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, latencyMs, fmt.Errorf("failed to parse ipify response: %w", err)
	}
	if result.IP == "" {
		return nil, latencyMs, fmt.Errorf("ipify: no IP found in response")
	}
	return &service.ProxyExitInfo{
		IP: result.IP,
	}, latencyMs, nil
}

// parseChatGPTTrace 解析 Cloudflare trace 端点（如 chatgpt.com/cdn-cgi/trace）的纯文本响应。
// 响应按行给出键值对，其中 ip= 为出口 IP，loc= 为国家代码。
func (s *proxyProbeService) parseChatGPTTrace(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var ip, loc string
	for _, line := range strings.Split(string(body), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "ip":
			ip = strings.TrimSpace(value)
		case "loc":
			loc = strings.TrimSpace(value)
		}
	}
	if ip == "" {
		preview := string(body)
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, latencyMs, fmt.Errorf("chatgpt-trace: no ip= found in response (body: %s)", preview)
	}
	info := &service.ProxyExitInfo{
		IP: ip,
	}
	if loc != "" {
		info.CountryCode = loc
	}
	return info, latencyMs, nil
}

// parseIPIPJSON parses https://myip.ipip.net/json
// {"ret":"ok","data":{"ip":"...","location":["中国","北京","北京","","ISP"]}}
func (s *proxyProbeService) parseIPIPJSON(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var result struct {
		Ret  string `json:"ret"`
		Data struct {
			IP       string   `json:"ip"`
			Location []string `json:"location"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, latencyMs, fmt.Errorf("failed to parse ipip-json response: %w", err)
	}
	if !strings.EqualFold(result.Ret, "ok") {
		return nil, latencyMs, fmt.Errorf("ipip-json request failed: ret=%q", result.Ret)
	}
	if strings.TrimSpace(result.Data.IP) == "" {
		return nil, latencyMs, fmt.Errorf("ipip-json: no IP found in response")
	}
	info := &service.ProxyExitInfo{IP: strings.TrimSpace(result.Data.IP)}
	if len(result.Data.Location) > 0 {
		info.Country = strings.TrimSpace(result.Data.Location[0])
	}
	if len(result.Data.Location) > 1 {
		info.Region = strings.TrimSpace(result.Data.Location[1])
	}
	if len(result.Data.Location) > 2 {
		info.City = strings.TrimSpace(result.Data.Location[2])
	}
	if strings.EqualFold(info.Country, "中国") || strings.EqualFold(info.Country, "china") {
		info.CountryCode = "CN"
	}
	return info, latencyMs, nil
}

// parseIP9 parses https://ip9.com.cn/get
// {"ret":200,"data":{"ip":"...","country":"中国","country_code":"cn","prov":"北京","city":"北京"}}
func (s *proxyProbeService) parseIP9(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var result struct {
		Ret  json.RawMessage `json:"ret"`
		Data struct {
			IP          string `json:"ip"`
			Country     string `json:"country"`
			CountryCode string `json:"country_code"`
			Prov        string `json:"prov"`
			City        string `json:"city"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, latencyMs, fmt.Errorf("failed to parse ip9 response: %w", err)
	}
	if !ip9RetOK(result.Ret) {
		return nil, latencyMs, fmt.Errorf("ip9 request failed: ret=%s", string(result.Ret))
	}
	if strings.TrimSpace(result.Data.IP) == "" {
		return nil, latencyMs, fmt.Errorf("ip9: no IP found in response")
	}
	return &service.ProxyExitInfo{
		IP:          strings.TrimSpace(result.Data.IP),
		Country:     strings.TrimSpace(result.Data.Country),
		CountryCode: strings.ToUpper(strings.TrimSpace(result.Data.CountryCode)),
		Region:      strings.TrimSpace(result.Data.Prov),
		City:        strings.TrimSpace(result.Data.City),
	}, latencyMs, nil
}

func ip9RetOK(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var asInt int
	if err := json.Unmarshal(raw, &asInt); err == nil {
		return asInt == 200
	}
	var asStr string
	if err := json.Unmarshal(raw, &asStr); err == nil {
		return asStr == "200" || strings.EqualFold(asStr, "ok")
	}
	return false
}

// parseQQIP2City parses https://r.inews.qq.com/api/ip2city
// {"ret":0,"ip":"...","country":"中国","province":"北京市","city":"北京市"}
func (s *proxyProbeService) parseQQIP2City(body []byte, latencyMs int64) (*service.ProxyExitInfo, int64, error) {
	var result struct {
		Ret      int    `json:"ret"`
		ErrMsg   string `json:"errMsg"`
		IP       string `json:"ip"`
		Country  string `json:"country"`
		Province string `json:"province"`
		City     string `json:"city"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, latencyMs, fmt.Errorf("failed to parse qq-ip2city response: %w", err)
	}
	if result.Ret != 0 {
		msg := result.ErrMsg
		if msg == "" {
			msg = fmt.Sprintf("ret=%d", result.Ret)
		}
		return nil, latencyMs, fmt.Errorf("qq-ip2city request failed: %s", msg)
	}
	if strings.TrimSpace(result.IP) == "" {
		return nil, latencyMs, fmt.Errorf("qq-ip2city: no IP found in response")
	}
	info := &service.ProxyExitInfo{
		IP:      strings.TrimSpace(result.IP),
		Country: strings.TrimSpace(result.Country),
		Region:  strings.TrimSpace(result.Province),
		City:    strings.TrimSpace(result.City),
	}
	if strings.EqualFold(info.Country, "中国") || strings.EqualFold(info.Country, "china") {
		info.CountryCode = "CN"
	}
	return info, latencyMs, nil
}
