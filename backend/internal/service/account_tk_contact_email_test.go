package service

import "testing"

func TestResolveAccountContactEmail_PrefersExtra(t *testing.T) {
	t.Parallel()
	got := ResolveAccountContactEmail(
		map[string]any{"email_address": " extra@example.com "},
		map[string]any{"email": "cred@example.com"},
	)
	if got != "extra@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyAccountContactEmail_WritesAndClears(t *testing.T) {
	t.Parallel()
	extra, creds, err := ApplyAccountContactEmail(map[string]any{}, map[string]any{}, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if extra["email_address"] != "user@example.com" || extra["email"] != "user@example.com" {
		t.Fatalf("extra not synced: %#v", extra)
	}
	if creds["email"] != "user@example.com" || creds["email_address"] != "user@example.com" {
		t.Fatalf("credentials not synced: %#v", creds)
	}

	extra, creds, err = ApplyAccountContactEmail(extra, creds, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 || len(creds) != 0 {
		t.Fatalf("expected cleared maps, extra=%#v creds=%#v", extra, creds)
	}
}

func TestApplyAccountContactEmail_RejectsInvalid(t *testing.T) {
	t.Parallel()
	_, _, err := ApplyAccountContactEmail(map[string]any{}, map[string]any{}, "not-an-email")
	if err == nil {
		t.Fatal("expected error")
	}
}
