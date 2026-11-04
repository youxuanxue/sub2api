package migrations

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("migration missing %q", needle)
		}
	}
	profile := loadCanonicalKiroProfile(t)
	for _, field := range []string{
		"cipher_suites", "curves", "point_formats", "signature_algorithms",
		"alpn_protocols", "supported_versions", "key_share_groups", "psk_modes", "extensions",
	} {
		encoded, err := json.Marshal(profile[field])
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(sql, string(encoded)) {
			t.Fatalf("migration missing canonical %s %s", field, encoded)
		}
	}
	digest := canonicalKiroRuntimeDigest(t, profile)
	const capturedDigest = "f8e00beb1fafe8e50057236d677a8a5ad811d62de104fbaa429a84620b5ddcf0"
	if digest != capturedDigest {
		t.Fatalf("runtime digest = %s, captured %s", digest, capturedDigest)
	}
	if !strings.Contains(sql, digest) {
		t.Fatalf("migration missing runtime digest %s", digest)
	}
	const previousDigest = "97340b4d2b32bb1ec8cb4d116794be8a69fa57dc525eeb1e7be724d9fec9deaa"
	if strings.Contains(sql, previousDigest) {
		t.Fatal("refresh still carries the 2.37.0 runtime digest")
	}
	if !strings.Contains(sql, "false,\n    true,") {
		t.Fatal("expected enable_grease=false and shuffle_extensions=true")
	}
	if strings.Contains(sql, "DELETE FROM tls_fingerprint_profiles") {
		t.Fatal("refresh must not delete a profile row")
	}
}

func canonicalKiroProfilePath(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		candidate := filepath.Join(dir, "deploy", "aws", "stage0", "tk_canonical_kiro_cli.json")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("tk_canonical_kiro_cli.json not found from the test working directory")
		}
		dir = parent
	}
}

func loadCanonicalKiroProfile(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(canonicalKiroProfilePath(t))
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var profile map[string]any
	if err := decoder.Decode(&profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func canonicalKiroRuntimeDigest(t *testing.T, profile map[string]any) string {
	t.Helper()
	fields := []string{
		"enable_grease", "shuffle_extensions", "cipher_suites", "curves",
		"point_formats", "signature_algorithms", "alpn_protocols",
		"supported_versions", "key_share_groups", "psk_modes", "extensions",
	}
	projection := make(map[string]any, len(fields))
	for _, field := range fields {
		value, ok := profile[field]
		if !ok {
			t.Fatalf("canonical profile missing %s", field)
		}
		projection[field] = value
	}
	if projection["shuffle_extensions"] != true {
		t.Fatal("canonical profile must shuffle extensions")
	}
	projection["extensions"] = sortedJSONNumbers(t, projection["extensions"])
	body, err := json.Marshal(projection)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func sortedJSONNumbers(t *testing.T, value any) []json.Number {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("extensions type %T", value)
	}
	numbers := make([]json.Number, len(items))
	for i, item := range items {
		number, ok := item.(json.Number)
		if !ok {
			t.Fatalf("extension type %T", item)
		}
		if _, err := number.Int64(); err != nil {
			t.Fatal(err)
		}
		numbers[i] = number
	}
	sort.Slice(numbers, func(i, j int) bool {
		left, _ := numbers[i].Int64()
		right, _ := numbers[j].Int64()
		return left < right
	})
	return numbers
}
