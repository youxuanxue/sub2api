package migrations

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestMigrationTK109SeedsCodexCLITLSProfileFromCanonicalJSON(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_109_codex_cli_tls_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	raw, err := os.ReadFile("../../deploy/aws/stage0/tk_canonical_codex_cli.json")
	if err != nil {
		t.Fatal(err)
	}
	var canonical map[string]any
	if err := json.Unmarshal(raw, &canonical); err != nil {
		t.Fatal(err)
	}
	if canonical["name"] != "tk_canonical_codex_cli" || !strings.Contains(sql, "'tk_canonical_codex_cli'") {
		t.Fatal("profile name drift between JSON and migration")
	}
	for _, field := range []string{"cipher_suites", "curves", "point_formats", "signature_algorithms", "alpn_protocols", "supported_versions", "key_share_groups", "psk_modes", "extensions"} {
		b, err := json.Marshal(canonical[field])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, "'"+string(b)+"'::jsonb") {
			t.Fatalf("migration %s drifted from canonical JSON %s", field, b)
		}
	}
	if canonical["enable_grease"] != false || canonical["shuffle_extensions"] != true || !strings.Contains(sql, "false,\n    true,") {
		t.Fatal("expected enable_grease=false and shuffle_extensions=true (rustls shuffles extension order)")
	}
	if !strings.Contains(sql, "ON CONFLICT (name) DO UPDATE") {
		t.Fatal("seed must be idempotent")
	}
}
