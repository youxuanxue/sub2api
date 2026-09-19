package migrations

import (
	"strings"
	"testing"
)

func TestMigrationTK097SeedsAntigravityCLITLSProfile(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_097_antigravity_cli_tls_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, needle := range []string{
		"tk_canonical_antigravity_cli",
		"ON CONFLICT (name) DO UPDATE",
		"[49195,49199,49196,49200,52393,52392,49161,49171,49162,49172,4865,4866,4867]",
		"[4588,4587,4589,29,23,24,25]",
		`["h2","http/1.1"]`,
		"[4588,29]",
		"[0,11,65281,23,18,5,10,13,50,16,43,51]",
		"03117a8ed39ef02427ebbc39f121275c",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("migration missing %q", needle)
		}
	}
	// enable_grease=false, shuffle_extensions=false appear consecutively in VALUES
	if !strings.Contains(sql, "false,\n    false,") {
		t.Fatal("expected enable_grease=false and shuffle_extensions=false")
	}
	if strings.Contains(sql, "shuffle_extensions = true") {
		t.Fatal("AG CLI profile must not shuffle extensions")
	}
}
