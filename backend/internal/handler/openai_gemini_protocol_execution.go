package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIGeminiForwardFunc func() (*service.ForwardResult, error)

func executeOpenAIGeminiRoute(
	profile protocolrouter.GeminiEndpointProfile,
	antigravity openAIGeminiForwardFunc,
	native openAIGeminiForwardFunc,
) (*service.OpenAIForwardResult, error) {
	result, err := service.ExecuteGeminiProtocolProfile(profile, antigravity, native)
	return service.OpenAIForwardResultFromForward(result), err
}
