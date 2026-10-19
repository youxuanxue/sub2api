package repository

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type ProxyProbeServiceSuite struct {
	suite.Suite
	ctx      context.Context
	proxySrv *httptest.Server
	prober   *proxyProbeService
}

func (s *ProxyProbeServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.prober = &proxyProbeService{
		allowPrivateHosts:   true,
		configuredProbeURLs: resolveProxyProbeTargets(nil),
	}
}

func (s *ProxyProbeServiceSuite) TearDownTest() {
	if s.proxySrv != nil {
		s.proxySrv.Close()
		s.proxySrv = nil
	}
}

func (s *ProxyProbeServiceSuite) setupProxyServer(handler http.HandlerFunc) {
	s.proxySrv = newLocalTestServer(s.T(), handler)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidProxyURL() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "://bad")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_UnsupportedProxyScheme() {
	_, _, err := s.prober.ProbeProxy(s.ctx, "ftp://127.0.0.1:1")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "failed to create proxy client")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPAPI() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 检查是否是 ip-api 请求
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"success","query":"1.2.3.4","city":"c","regionName":"r","country":"cc","countryCode":"CC"}`)
			return
		}
		// 其他请求返回错误
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "c", info.City)
	require.Equal(s.T(), "r", info.Region)
	require.Equal(s.T(), "cc", info.Country)
	require.Equal(s.T(), "CC", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_Success_IPifyFallback() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ip-api 失败
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		// ipify 成功
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ip": "5.6.7.8"}`)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	info, latencyMs, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.NoError(s.T(), err, "ProbeProxy should fallback to ipify")
	require.GreaterOrEqual(s.T(), latencyMs, int64(0), "unexpected latency")
	require.Equal(s.T(), "5.6.7.8", info.IP)
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_AllFailed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_InvalidJSON() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.RequestURI, "ip-api.com") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		// ipify 也返回无效响应
		if strings.Contains(r.RequestURI, "api64.ipify.org") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, "not-json")
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "all probe URLs failed")
}

func (s *ProxyProbeServiceSuite) TestProbeProxy_ProxyServerClosed() {
	s.setupProxyServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.proxySrv.Close()

	_, _, err := s.prober.ProbeProxy(s.ctx, s.proxySrv.URL)
	require.Error(s.T(), err, "expected error when proxy server is closed")
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Success() {
	body := []byte(`{"status":"success","query":"1.2.3.4","city":"Beijing","regionName":"Beijing","country":"China","countryCode":"CN"}`)
	info, latencyMs, err := s.prober.parseIPAPI(body, 100)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(100), latencyMs)
	require.Equal(s.T(), "1.2.3.4", info.IP)
	require.Equal(s.T(), "Beijing", info.City)
	require.Equal(s.T(), "Beijing", info.Region)
	require.Equal(s.T(), "China", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseIPAPI_Failure() {
	body := []byte(`{"status":"fail","message":"rate limited"}`)
	_, _, err := s.prober.parseIPAPI(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "rate limited")
}

func (s *ProxyProbeServiceSuite) TestParseIPify_Success() {
	body := []byte(`{"ip": "2001:db8::1"}`)
	info, latencyMs, err := s.prober.parseIPify(body, 50)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(50), latencyMs)
	require.Equal(s.T(), "2001:db8::1", info.IP)
}

func (s *ProxyProbeServiceSuite) TestParseIPify_NoIP() {
	body := []byte(`{"ip": ""}`)
	_, _, err := s.prober.parseIPify(body, 50)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "no IP found")
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_Success() {
	body := []byte("fl=abc\nh=chatgpt.com\nip=203.0.113.5\nts=1700000000\nloc=US\ntz=UTC\n")
	info, latencyMs, err := s.prober.parseChatGPTTrace(body, 320)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(320), latencyMs)
	require.Equal(s.T(), "203.0.113.5", info.IP)
	require.Equal(s.T(), "US", info.CountryCode)
}

func (s *ProxyProbeServiceSuite) TestParseChatGPTTrace_NoIP() {
	body := []byte("fl=abc\nh=chatgpt.com\nloc=US\n")
	_, _, err := s.prober.parseChatGPTTrace(body, 100)
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "chatgpt-trace: no ip= found")
}

func (s *ProxyProbeServiceSuite) TestParseIPIPJSON_Success() {
	body := []byte(`{"ret":"ok","data":{"ip":"106.75.24.120","location":["中国","北京","北京","","联通"]}}`)
	info, latencyMs, err := s.prober.parseIPIPJSON(body, 90)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(90), latencyMs)
	require.Equal(s.T(), "106.75.24.120", info.IP)
	require.Equal(s.T(), "中国", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
	require.Equal(s.T(), "北京", info.Region)
	require.Equal(s.T(), "北京", info.City)
}

func (s *ProxyProbeServiceSuite) TestParseIPIPJSON_NotOK() {
	_, _, err := s.prober.parseIPIPJSON([]byte(`{"ret":"fail","data":{}}`), 10)
	require.ErrorContains(s.T(), err, "ipip-json request failed")
}

func (s *ProxyProbeServiceSuite) TestParseIP9_Success() {
	body := []byte(`{"ret":200,"data":{"ip":"106.75.24.120","country":"中国","country_code":"cn","prov":"上海","city":"杨浦"}}`)
	info, latencyMs, err := s.prober.parseIP9(body, 110)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(110), latencyMs)
	require.Equal(s.T(), "106.75.24.120", info.IP)
	require.Equal(s.T(), "中国", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
	require.Equal(s.T(), "上海", info.Region)
	require.Equal(s.T(), "杨浦", info.City)
}

func (s *ProxyProbeServiceSuite) TestParseIP9_BadRet() {
	_, _, err := s.prober.parseIP9([]byte(`{"ret":500,"data":{"ip":"1.1.1.1"}}`), 10)
	require.ErrorContains(s.T(), err, "ip9 request failed")
}

func (s *ProxyProbeServiceSuite) TestParseQQIP2City_Success() {
	body := []byte(`{"ret":0,"errMsg":"","ip":"106.75.24.120","country":"中国","province":"上海市","city":"杨浦区"}`)
	info, latencyMs, err := s.prober.parseQQIP2City(body, 70)
	require.NoError(s.T(), err)
	require.Equal(s.T(), int64(70), latencyMs)
	require.Equal(s.T(), "106.75.24.120", info.IP)
	require.Equal(s.T(), "中国", info.Country)
	require.Equal(s.T(), "CN", info.CountryCode)
	require.Equal(s.T(), "上海市", info.Region)
	require.Equal(s.T(), "杨浦区", info.City)
}

func (s *ProxyProbeServiceSuite) TestParseQQIP2City_BadRet() {
	_, _, err := s.prober.parseQQIP2City([]byte(`{"ret":1,"errMsg":"busy","ip":"1.1.1.1"}`), 10)
	require.ErrorContains(s.T(), err, "busy")
}

func TestResolveProxyProbeTargets_PresetDomestic(t *testing.T) {
	t.Parallel()
	targets := resolveProxyProbeTargets(&config.Config{
		Security: config.SecurityConfig{
			ProxyProbe: config.ProxyProbeConfig{Preset: "domestic"},
		},
	})
	require.NotEmpty(t, targets)
	require.Equal(t, "ipip-json", targets[0].parser)
	require.Contains(t, targets[0].url, "ipip.net")
}

func TestResolveProxyProbeTargets_ExplicitURLsWin(t *testing.T) {
	t.Parallel()
	targets := resolveProxyProbeTargets(&config.Config{
		Security: config.SecurityConfig{
			ProxyProbe: config.ProxyProbeConfig{
				Preset: "domestic",
				URLs: []config.ProbeURLConfig{
					{URL: "https://example.com/ip", Parser: "ipify"},
				},
			},
		},
	})
	require.Len(t, targets, 1)
	require.Equal(t, "https://example.com/ip", targets[0].url)
	require.Equal(t, "ipify", targets[0].parser)
}

func TestResolveProxyProbeTargets_DefaultOverseas(t *testing.T) {
	t.Parallel()
	targets := resolveProxyProbeTargets(nil)
	require.Equal(t, "ip-api", targets[0].parser)
}

func TestProxyProbeServiceSuite(t *testing.T) {
	suite.Run(t, new(ProxyProbeServiceSuite))
}
