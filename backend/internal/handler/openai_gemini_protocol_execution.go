package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type openAIGeminiForwardFunc func() (*service.ForwardResult, error)

func executeOpenAIGeminiRoute(
	profile protocolrouter.GeminiEndpointProfile,
	antigravity openAIGeminiForwardFunc,
	vertex openAIGeminiForwardFunc,
) (*service.OpenAIForwardResult, error) {
	result, err := service.ExecuteGeminiProtocolProfile(profile, antigravity, vertex)
	return service.OpenAIForwardResultFromForward(result), err
}
