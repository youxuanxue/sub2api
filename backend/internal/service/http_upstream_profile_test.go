package service

import (
	"context"
	"testing"
)

func TestWithHTTPUpstreamProfile_DefaultKeepsContext(t *testing.T) {
	ctx := context.Background()
	got := WithHTTPUpstreamProfile(ctx, HTTPUpstreamProfileDefault)
	if got != ctx {
		t.Fatal("default profile should not wrap context")
	}
}

func TestWithHTTPUpstreamProfile_OpenAI(t *testing.T) {
	ctx := WithHTTPUpstreamProfile(context.TODO(), HTTPUpstreamProfileOpenAI)
	if profile := HTTPUpstreamProfileFromContext(ctx); profile != HTTPUpstreamProfileOpenAI {
		t.Fatalf("expected profile %q, got %q", HTTPUpstreamProfileOpenAI, profile)
	}
}

func TestGrokOfficialAPIFallbackAllowedContext(t *testing.T) {
	if !GrokOfficialAPIFallbackAllowedFromContext(context.Background()) {
		t.Fatal("missing context value should default to allowed")
	}
	ctx := WithGrokOfficialAPIFallbackAllowed(context.Background(), false)
	if GrokOfficialAPIFallbackAllowedFromContext(ctx) {
		t.Fatal("explicit false must disable fallback")
	}
}
