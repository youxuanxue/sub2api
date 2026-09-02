package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestResolveSeedanceClientAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		in        string
		want      string
		wantAlias bool
	}{
		{"hyphen 1.5-pro", "doubao-seedance-1-5-pro", "doubao-seedance-1-5-pro-251215", true},
		{"dotted 1.5-pro", "doubao-seedance-1.5-pro", "doubao-seedance-1-5-pro-251215", true},
		{"hyphen 2.0", "doubao-seedance-2-0", "doubao-seedance-2-0-260128", true},
		{"dotted 2.0", "doubao-seedance-2.0", "doubao-seedance-2-0-260128", true},
		{"hyphen 2.0-fast", "doubao-seedance-2-0-fast", "doubao-seedance-2-0-fast-260128", true},
		{"dotted 2.0-fast", "doubao-seedance-2.0-fast", "doubao-seedance-2-0-fast-260128", true},
		{"hyphen 2.5", "doubao-seedance-2-5", "doubao-seedance-2-5-260628", true},
		{"dotted 2.5", "doubao-seedance-2.5", "doubao-seedance-2-5-260628", true},
		{"case fold", "Doubao-Seedance-1.5-Pro", "doubao-seedance-1-5-pro-251215", true},
		{"dated 1.5-pro stays", "doubao-seedance-1-5-pro-251215", "", false},
		{"dated 2.0 stays", "doubao-seedance-2-0-260128", "", false},
		{"dated 2.5 stays", "doubao-seedance-2-5-260628", "", false},
		{"mini is not 2.0", "doubao-seedance-2.0-mini", "", false},
		{"unknown stays", "doubao-seedance-3-0", "", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := ResolveSeedanceClientAlias(tc.in)
			if ok != tc.wantAlias {
				t.Fatalf("ResolveSeedanceClientAlias(%q) ok=%v, want %v", tc.in, ok, tc.wantAlias)
			}
			if tc.wantAlias && got != tc.want {
				t.Fatalf("resolved = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestApplySeedanceClientAlias_RewritesBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"doubao-seedance-2-0","prompt":"a cat","duration":5}`)
	got, resolved, ok := ApplySeedanceClientAlias(body, "doubao-seedance-2-0")
	if !ok || resolved != "doubao-seedance-2-0-260128" {
		t.Fatalf("ok=%v resolved=%q", ok, resolved)
	}
	if gjson.GetBytes(got, "model").String() != "doubao-seedance-2-0-260128" {
		t.Fatalf("body model = %q", gjson.GetBytes(got, "model").String())
	}
	if gjson.GetBytes(got, "prompt").String() != "a cat" || gjson.GetBytes(got, "duration").Int() != 5 {
		t.Fatalf("alias rewrite must only touch model, got %s", got)
	}
}

func TestApplySeedanceClientAlias_LeavesForeignBody(t *testing.T) {
	t.Parallel()
	body := []byte(`{"model":"doubao-seedance-2.0-mini","prompt":"x"}`)
	got, resolved, ok := ApplySeedanceClientAlias(body, "doubao-seedance-2.0-mini")
	if ok || resolved != "doubao-seedance-2.0-mini" || string(got) != string(body) {
		t.Fatalf("mini must not alias: ok=%v resolved=%q body=%s", ok, resolved, got)
	}
}
