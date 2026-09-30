package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
)

// UsesGeminiImagesAdapter selects the facade by request operation and model
// family. The actual account still requires the native Gemini protocol Plan.
func UsesGeminiImagesAdapter(shape UniversalShape, model string) bool {
	return shape == ShapeOpenAIImages && antigravity.IsImageModel(model)
}

func PrepareGeminiImagesRequest(body []byte) (string, []byte, error) {
	return apicompat.ImagesToGeminiGeneration(body)
}

// Keep existing Images providers on their original media contract. Only an AG
// candidate executes the Gemini request constructed by the operation adapter.
func geminiImagesCandidateContext(ctx context.Context, shape UniversalShape, model string, account *Account) context.Context {
	if UsesGeminiImagesAdapter(shape, model) && account != nil && account.Platform != PlatformAntigravity {
		return context.WithValue(ctx, protocolRoutingContextKey{}, false)
	}
	return ctx
}
