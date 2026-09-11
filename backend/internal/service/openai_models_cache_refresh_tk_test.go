package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIModelsRefreshRechecksCacheAfterConcurrentFill(t *testing.T) {
	for _, test := range []struct {
		name      string
		age       time.Duration
		wantCalls int32
	}{
		{name: "fresh concurrent fill"},
		{name: "stale still revalidates", age: openAIModelsCacheTTL + time.Second, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := &OpenAIGatewayService{}
			request := openAIModelsRequest{url: "https://models.example/v1/models"}
			key := buildOpenAIModelsCacheKey(request)
			_, state := s.openAIModelsCache.get(key, time.Now())
			require.Equal(t, openAIModelsCacheMiss, state)

			// Another caller completes its flight after our miss, before we join.
			cached := &OpenAIModelsResponse{Body: []byte(`{"data":[]}`), upstreamETag: `"catalog-v1"`}
			s.openAIModelsCache.set(key, cached, time.Now().Add(-test.age))
			var calls atomic.Int32
			var receivedETag string
			result := <-s.refreshCachedOpenAIModels(key, request, func(_ context.Context, etag string) (*OpenAIModelsResponse, error) {
				calls.Add(1)
				receivedETag = etag
				return &OpenAIModelsResponse{NotModified: true}, nil
			})
			require.NoError(t, result.Err)
			require.Same(t, cached, result.Val)
			require.Equal(t, test.wantCalls, calls.Load())
			if test.wantCalls > 0 {
				require.Equal(t, cached.upstreamETag, receivedETag)
			}
			_, state = s.openAIModelsCache.get(key, time.Now())
			require.Equal(t, openAIModelsCacheFresh, state)
		})
	}
}
