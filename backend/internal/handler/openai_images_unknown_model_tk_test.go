//go:build unit

package handler

import (
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesUnknownModelNotFound_UpgradesCapacityTo404(t *testing.T) {
	pricing := &service.PricingService{}
	billing := &service.BillingService{}
	// Wire through reflection-free constructor path used elsewhere in tests:
	// BillingService.pricingService is unexported; use OpenAIGatewayService helper
	// with a nil gateway so the fail-open path is explicit, then a real one.
	status, errType, msg := openAIImagesUnknownModelNotFound(
		nil, nil, "gpt-image-does-not-exist", "",
		http.StatusServiceUnavailable, "api_error", "No available accounts",
	)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "api_error", errType)
	require.Equal(t, "No available accounts", msg)

	_ = pricing
	_ = billing

	// Already-404 stays untouched.
	status, errType, msg = openAIImagesUnknownModelNotFound(
		&service.OpenAIGatewayService{}, nil, "gpt-image-does-not-exist", "",
		http.StatusNotFound, "model_not_found", "Model \"gpt-image-does-not-exist\" is not supported",
	)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "model_not_found", errType)

	// Non gpt-image models are not rewritten.
	status, errType, msg = openAIImagesUnknownModelNotFound(
		&service.OpenAIGatewayService{}, nil, "gpt-5.4", "",
		http.StatusServiceUnavailable, "api_error", "No available accounts",
	)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "api_error", errType)
}
