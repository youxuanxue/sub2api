package logredact

import "testing"

func TestRedactAssignmentScannerOrdering(t *testing.T) {
	for _, input := range []string{
		`"token":"token" : "hidden"`, `"token"="hidden"`, `"token":"secret"token=hidden`,
		`token=,secret&safe=yes`, `token=,secret&password=hidden`,
		`token=abc,"password":"a b"`, `token="password":"a b"`,
		`token=abc&safe="password":"a b"`,
	} {
		if got, want := redactUnstructuredText(input, defaultTextRedactPatterns), redactUnstructuredReference(input, defaultTextRedactPatterns); got != want {
			t.Errorf("input %q: got %q, want %q", input, got, want)
		}
	}
}

func TestRedactAssignmentExtraKeysMatchOriginal(t *testing.T) {
	for _, key := range []string{"-x", "x-", "a-b", "key", "custom"} {
		p := getTextRedactPatterns([]string{key})
		for _, input := range []string{key + "=hidden", "prefix-" + key + "=hidden", "\"" + key + "\":\"hidden\""} {
			if got, want := redactUnstructuredText(input, p), redactUnstructuredReference(input, p); got != want {
				t.Fatalf("key %q, input %q: got %q, want %q", key, input, got, want)
			}
		}
	}
}
