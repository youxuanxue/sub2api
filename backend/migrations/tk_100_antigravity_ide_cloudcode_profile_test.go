package migrations

import (
	"strings"
	"testing"
)

func TestMigrationTK100SeedsOfficialIDECloudcodeProfile(t *testing.T) {
	content, err := FS.ReadFile("tk_100_antigravity_ide_cloudcode_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, want := range []string{
		"tk_canonical_antigravity_ide_cloudcode",
		"cloudcode-pa.googleapis.com",
		"'[]'::jsonb",
		"[0,11,65281,23,18,5,10,13,50,43,51]",
		"ON CONFLICT (name) DO UPDATE",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	if strings.Contains(sql, ",16,43,51]") {
		t.Fatal("official IDE cloudcode profile must not seed the ALPN extension")
	}
}
