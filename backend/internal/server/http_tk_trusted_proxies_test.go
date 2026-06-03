//go:build unit

package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/gin-gonic/gin"
)

func TestTkResolveTrustedProxies(t *testing.T) {
	cases := []struct {
		name      string
		in        []string
		wantTrust bool
		want      []string
	}{
		{
			name:      "empty defaults to private ranges",
			in:        nil,
			wantTrust: true,
			want:      ip.PrivateCIDRs,
		},
		{
			name:      "all blank defaults to private ranges",
			in:        []string{"", "   "},
			wantTrust: true,
			want:      ip.PrivateCIDRs,
		},
		{
			name:      "explicit list wins",
			in:        []string{"203.0.113.0/24", " 198.51.100.7 "},
			wantTrust: true,
			want:      []string{"203.0.113.0/24", "198.51.100.7"},
		},
		{
			name:      "opt-out sentinel none disables trust",
			in:        []string{"none"},
			wantTrust: false,
			want:      nil,
		},
		{
			name:      "opt-out sentinel is case-insensitive",
			in:        []string{"OFF"},
			wantTrust: false,
			want:      nil,
		},
		{
			name:      "sentinel mixed with addresses still disables trust",
			in:        []string{"10.0.0.0/8", "Disabled"},
			wantTrust: false,
			want:      nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, trust := tkResolveTrustedProxies(tc.in)
			if trust != tc.wantTrust {
				t.Fatalf("trust = %v, want %v", trust, tc.wantTrust)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("proxies = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTkTrustedProxiesClientIPResolution 证明在默认私网信任下，gin 的 c.ClientIP()：
//  1. 反代后（对端为私网跳点）沿 X-Forwarded-For 解析出真实公网客户端；
//  2. 直连公网（对端为公网地址）忽略伪造的 X-Forwarded-For，返回真实对端。
func TestTkTrustedProxiesClientIPResolution(t *testing.T) {
	gin.SetMode(gin.TestMode)

	proxies, trust := tkResolveTrustedProxies(nil)
	if !trust {
		t.Fatal("expected default to trust private ranges")
	}

	cases := []struct {
		name       string
		remoteAddr string // gin 看到的直接 TCP 对端
		xff        string
		wantIP     string
	}{
		{
			name:       "behind proxy resolves real client from XFF",
			remoteAddr: "172.18.0.2:54321", // docker bridge 跳点（受信）
			xff:        "203.0.113.9",
			wantIP:     "203.0.113.9",
		},
		{
			name:       "behind proxy strips trusted hops in XFF",
			remoteAddr: "127.0.0.1:1",
			xff:        "203.0.113.9, 10.1.2.3", // 末跳为私网受信，应被跳过
			wantIP:     "203.0.113.9",
		},
		{
			name:       "direct public peer ignores spoofed XFF",
			remoteAddr: "198.51.100.50:443", // 公网对端（不受信）
			xff:        "1.2.3.4",            // 伪造
			wantIP:     "198.51.100.50",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := gin.New()
			if err := r.SetTrustedProxies(proxies); err != nil {
				t.Fatalf("SetTrustedProxies: %v", err)
			}
			var got string
			r.GET("/", func(c *gin.Context) {
				got = c.ClientIP()
				c.Status(http.StatusOK)
			})

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remoteAddr
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			r.ServeHTTP(httptest.NewRecorder(), req)

			if got != tc.wantIP {
				t.Fatalf("ClientIP = %q, want %q", got, tc.wantIP)
			}
		})
	}
}
