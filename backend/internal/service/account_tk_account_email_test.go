package service

import (
	"net/http"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func TestResolveAccountEmail_PrefersExtra(t *testing.T) {
	t.Parallel()
	got := ResolveAccountEmail(
		map[string]any{"email_address": " extra@example.com "},
		map[string]any{"email": "cred@example.com"},
	)
	if got != "extra@example.com" {
		t.Fatalf("got %q", got)
	}
}

func TestApplyAccountEmail_WritesAndClears(t *testing.T) {
	t.Parallel()
	extra, creds, err := ApplyAccountEmail(map[string]any{}, map[string]any{}, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if extra["email_address"] != "user@example.com" || extra["email"] != "user@example.com" {
		t.Fatalf("extra not synced: %#v", extra)
	}
	if creds["email"] != "user@example.com" || creds["email_address"] != "user@example.com" {
		t.Fatalf("credentials not synced: %#v", creds)
	}

	extra, creds, err = ApplyAccountEmail(extra, creds, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(extra) != 0 || len(creds) != 0 {
		t.Fatalf("expected cleared maps, extra=%#v creds=%#v", extra, creds)
	}
}

func TestApplyAccountEmail_RejectsInvalid(t *testing.T) {
	t.Parallel()
	_, _, err := ApplyAccountEmail(map[string]any{}, map[string]any{}, "not-an-email")
	if err == nil {
		t.Fatal("expected error")
	}
	if !infraerrors.IsBadRequest(err) {
		t.Fatalf("expected bad request, got %T: %v", err, err)
	}
	code, _ := infraerrors.ToHTTP(err)
	if code != http.StatusBadRequest {
		t.Fatalf("expected HTTP 400, got %d", code)
	}
}
