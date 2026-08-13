//go:build integration

package tlsfingerprint

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

type kiroReplayProfile struct {
	Name                string   `json:"name"`
	EnableGREASE        bool     `json:"enable_grease"`
	ShuffleExtensions   bool     `json:"shuffle_extensions"`
	CipherSuites        []uint16 `json:"cipher_suites"`
	Curves              []uint16 `json:"curves"`
	PointFormats        []uint16 `json:"point_formats"`
	SignatureAlgorithms []uint16 `json:"signature_algorithms"`
	ALPNProtocols       []string `json:"alpn_protocols"`
	SupportedVersions   []uint16 `json:"supported_versions"`
	KeyShareGroups      []uint16 `json:"key_share_groups"`
	PSKModes            []uint16 `json:"psk_modes"`
	Extensions          []uint16 `json:"extensions"`
}

func (p kiroReplayProfile) runtimeProfile() *Profile {
	return &Profile{
		Name:                p.Name,
		EnableGREASE:        p.EnableGREASE,
		ShuffleExtensions:   p.ShuffleExtensions,
		CipherSuites:        p.CipherSuites,
		Curves:              p.Curves,
		PointFormats:        p.PointFormats,
		SignatureAlgorithms: p.SignatureAlgorithms,
		ALPNProtocols:       p.ALPNProtocols,
		SupportedVersions:   p.SupportedVersions,
		KeyShareGroups:      p.KeyShareGroups,
		PSKModes:            p.PSKModes,
		Extensions:          p.Extensions,
	}
}

func TestKiroCanonicalProfileReplay(t *testing.T) {
	profilePath := os.Getenv("KIRO_REPLAY_PROFILE")
	proxyRaw := os.Getenv("KIRO_REPLAY_PROXY")
	if profilePath == "" || proxyRaw == "" {
		t.Skip("set KIRO_REPLAY_PROFILE and KIRO_REPLAY_PROXY to run the real replay gate")
	}

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatalf("read replay profile: %v", err)
	}
	var persisted kiroReplayProfile
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatalf("parse replay profile: %v", err)
	}
	if persisted.Name != "tk_canonical_kiro_cli" || !persisted.ShuffleExtensions {
		t.Fatalf("replay requires the canonical shuffled Kiro CLI profile, got %+v", persisted)
	}
	proxyURL, err := url.Parse(proxyRaw)
	if err != nil {
		t.Fatalf("parse replay proxy: %v", err)
	}

	const samples = 3
	for sample := 1; sample <= samples; sample++ {
		dialer := NewHTTPProxyDialer(persisted.runtimeProfile(), proxyURL)
		client := &http.Client{
			Transport: &http.Transport{
				DialTLSContext:    dialer.DialTLSContext,
				DisableKeepAlives: true,
			},
			Timeout: 30 * time.Second,
		}
		req, err := http.NewRequest(http.MethodGet, "https://codewhisperer.us-east-1.amazonaws.com/", nil)
		if err != nil {
			t.Fatalf("build replay request %d: %v", sample, err)
		}
		resp, err := client.Do(req)
		if err != nil {
			if strings.Contains(err.Error(), "certificate is not trusted") {
				t.Logf("replay sample %d emitted ClientHello before the capture CA rejection", sample)
				continue
			}
			t.Fatalf("replay request %d: %v", sample, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close replay response %d: %v", sample, err)
		}
		t.Logf("replay sample %d completed with HTTP %s", sample, fmt.Sprint(resp.StatusCode))
	}
}
