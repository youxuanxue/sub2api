package migrations

import (
	"strings"
	"testing"
)

func TestMigrationTK099SeedsOptInAntigravityManagerProfile(t *testing.T) {
	content, err := FS.ReadFile("tk_099_antigravity_manager_chrome_profile.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, want := range []string{
		"tk_canonical_antigravity_manager_chrome123",
		"Chrome123",
		"Chrome120",
		"ON CONFLICT (name) DO UPDATE",
		"[\"h2\",\"http/1.1\"]",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
}
