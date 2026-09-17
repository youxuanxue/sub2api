package config

import "testing"

func TestNormalizeServerPprofListen(t *testing.T) {
	t.Parallel()

	t.Run("disabled_tokens", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"", " ", "off", "OFF", "disabled", "false", "-"} {
			addr, enabled, err := NormalizeServerPprofListen(raw)
			if err != nil {
				t.Fatalf("%q: unexpected err %v", raw, err)
			}
			if enabled || addr != "" {
				t.Fatalf("%q: want disabled, got enabled=%v addr=%q", raw, enabled, addr)
			}
		}
	})

	t.Run("accepts_loopback", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{"127.0.0.1:6060", "[::1]:6060"} {
			addr, enabled, err := NormalizeServerPprofListen(raw)
			if err != nil {
				t.Fatalf("%q: %v", raw, err)
			}
			if !enabled || addr == "" {
				t.Fatalf("%q: want enabled addr, got enabled=%v addr=%q", raw, enabled, addr)
			}
		}
	})

	t.Run("rejects_non_loopback_and_wildcard", func(t *testing.T) {
		t.Parallel()
		for _, raw := range []string{":6060", "0.0.0.0:6060", "[::]:6060", "8.8.8.8:6060", "localhost"} {
			_, enabled, err := NormalizeServerPprofListen(raw)
			if err == nil || enabled {
				t.Fatalf("%q: want error, got enabled=%v err=%v", raw, enabled, err)
			}
		}
	})
}
