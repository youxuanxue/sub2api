package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
)

// hydratedGroup builds a context-valid Group (passes IsGroupContextValid) with
// the given cc-only flag, so tkGroupAdmitsNonCC resolves it from context without
// a repository.
func hydratedGroup(id int64, ccOnly bool) *Group {
	return &Group{
		ID:             id,
		Platform:       PlatformAnthropic,
		Status:         "active",
		Hydrated:       true,
		ClaudeCodeOnly: ccOnly,
	}
}

func TestTkGroupAdmitsNonCC(t *testing.T) {
	s := &GatewayService{}
	gid := int64(7)

	t.Run("cc_only=false admits non-cc", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkey.Group, hydratedGroup(gid, false))
		if !s.tkGroupAdmitsNonCC(ctx, &ParsedRequest{GroupID: &gid}) {
			t.Fatal("cc_only=false group must admit non-cc")
		}
	})

	t.Run("cc_only=true keeps the guard", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), ctxkey.Group, hydratedGroup(gid, true))
		if s.tkGroupAdmitsNonCC(ctx, &ParsedRequest{GroupID: &gid}) {
			t.Fatal("cc_only=true group must NOT admit non-cc")
		}
	})

	t.Run("nil parsed fails closed", func(t *testing.T) {
		if s.tkGroupAdmitsNonCC(context.Background(), nil) {
			t.Fatal("nil parsed must fail closed (keep guard)")
		}
	})

	t.Run("nil group id fails closed", func(t *testing.T) {
		if s.tkGroupAdmitsNonCC(context.Background(), &ParsedRequest{}) {
			t.Fatal("missing group id must fail closed (keep guard)")
		}
	})
}
