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

type geminiImagesExecutionKey struct{}

// WithGeminiImagesExecution binds reselection to the dispatched Images protocol.
// Admission still considers all providers; the native and Images handlers cannot
// execute each other's wire format once dispatch has selected one of them.
func WithGeminiImagesExecution(ctx context.Context) context.Context {
	request := CandidateRequestFromContext(ctx)
	if request == nil || !UsesGeminiImagesAdapter(request.shape, request.model) {
		return ctx
	}
	platform, ok := CandidateExecutionPlatform(ctx)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, geminiImagesExecutionKey{}, platform == PlatformAntigravity)
}
