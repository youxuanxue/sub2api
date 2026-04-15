package handler

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestWithAffinityPrefetchedSession_Hit(t *testing.T) {
	orig := openAIGetPreferredAccountByAffinity
	t.Cleanup(func() {
		openAIGetPreferredAccountByAffinity = orig
	})

	called := false
	openAIGetPreferredAccountByAffinity = func(c *gin.Context, modelName string, groupName string) (int64, bool) {
		called = true
		if modelName != "gpt-4o-mini" || groupName != "g-openai" {
			t.Fatalf("unexpected affinity query: model=%s group=%s", modelName, groupName)
		}
		return 101, true
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	groupID := int64(9)

	h := &OpenAIGatewayHandler{}
	ctx := h.withAffinityPrefetchedSession(context.Background(), c, &groupID, "g-openai", "gpt-4o-mini")
	if !called {
		t.Fatal("expected affinity query to be called")
	}

	gotAccountID, ok := service.PrefetchedStickyAccountIDFromContext(ctx)
	if !ok || gotAccountID != 101 {
		t.Fatalf("expected prefetched account id 101, got %d (ok=%v)", gotAccountID, ok)
	}

	gotGroupID, ok := service.PrefetchedStickyGroupIDFromContext(ctx)
	if !ok || gotGroupID != 9 {
		t.Fatalf("expected prefetched group id 9, got %d (ok=%v)", gotGroupID, ok)
	}
}

func TestWithAffinityPrefetchedSession_Miss(t *testing.T) {
	orig := openAIGetPreferredAccountByAffinity
	t.Cleanup(func() {
		openAIGetPreferredAccountByAffinity = orig
	})

	openAIGetPreferredAccountByAffinity = func(c *gin.Context, modelName string, groupName string) (int64, bool) {
		return 0, false
	}

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	groupID := int64(9)

	h := &OpenAIGatewayHandler{}
	ctx := h.withAffinityPrefetchedSession(context.Background(), c, &groupID, "g-openai", "gpt-4o-mini")

	if _, ok := service.PrefetchedStickyAccountIDFromContext(ctx); ok {
		t.Fatal("did not expect prefetched account id on affinity miss")
	}
	if _, ok := service.PrefetchedStickyGroupIDFromContext(ctx); ok {
		t.Fatal("did not expect prefetched group id on affinity miss")
	}
}
