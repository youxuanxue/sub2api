package migrations

import (
	"strings"
	"testing"
)

func TestMigrationTK108RefreshesKiroCLITLSProfile(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_108_kiro_cli_tls_profile_refresh.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, needle := range []string{
		"tk_canonical_kiro_cli",
		"ON CONFLICT (name) DO UPDATE",
		"[4866,4865,4867,49196,49195,52393,49200,49199,52392,255]",
		"[4588,29,23,24]",
		"[1283,1027,1539,2055,2054,2053,2052,1537,1281,1025,2308,2309,2310]",
		"[4588,29]",
		"[0,5,10,11,13,23,35,43,45,51]",
		"f8e00beb1fafe8e50057236d677a8a5ad811d62de104fbaa429a84620b5ddcf0",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("migration missing %q", needle)
		}
	}
	if !strings.Contains(sql, "false,\n    true,") {
		t.Fatal("expected enable_grease=false and shuffle_extensions=true")
	}
	if strings.Contains(sql, "DELETE FROM tls_fingerprint_profiles") {
		t.Fatal("refresh must not delete a profile row")
	}
}
