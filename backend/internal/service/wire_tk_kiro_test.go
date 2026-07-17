//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProvideRateLimitService_KiroOAuth401RefreshAPIWired(t *testing.T) {
	refreshAPI := NewOAuthRefreshAPI(nil, nil)

	svc := ProvideRateLimitService(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		refreshAPI,
	)

	require.Same(t, refreshAPI, svc.oauthRefreshAPI)
}
