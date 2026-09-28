package geminiweb

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/stretchr/testify/require"
)

func TestEstimateUsageCountsRequestAndResponseText(t *testing.T) {
	req := []byte(`{
		"systemInstruction":{"parts":[{"text":"You are helpful."}]},
		"contents":[{"role":"user","parts":[{"text":"Hello world from gemini web"}]}]
	}`)
	resp := []byte(`{
		"candidates":[{"content":{"parts":[{"text":"Hi there, friend."}]}}]
	}`)

	got := EstimateUsage(req, resp, "")
	wantIn := tokenestimate.Count("You are helpful.\nHello world from gemini web")
	wantOut := tokenestimate.Count("Hi there, friend.")
	require.Equal(t, wantIn, got.Input)
	require.Equal(t, wantOut, got.Output)
	require.Greater(t, got.Input, 0)
	require.Greater(t, got.Output, 0)
}

func TestEstimateUsageOutputTextOverridesResponseBody(t *testing.T) {
	req := []byte(`{"contents":[{"parts":[{"text":"ping"}]}]}`)
	resp := []byte(`{"candidates":[{"content":{"parts":[{"text":"from-body"}]}}]}`)
	got := EstimateUsage(req, resp, "streamed-output-text")
	require.Equal(t, tokenestimate.Count("streamed-output-text"), got.Output)
	require.NotEqual(t, tokenestimate.Count("from-body"), got.Output)
}

func TestEstimateInputSkipsInlineImageData(t *testing.T) {
	req := []byte(`{
		"contents":[{"parts":[
			{"text":"describe this"},
			{"inlineData":{"mimeType":"image/png","data":"AAAA"}}
		]}]
	}`)
	got := EstimateInputTokens(req)
	require.Equal(t, tokenestimate.Count("describe this"), got)
}

func TestEstimateInputIncludesToolDeclarations(t *testing.T) {
	req := []byte(`{
		"contents":[{"parts":[{"text":"call tool"}]}],
		"tools":[{"functionDeclarations":[{"name":"lookup","description":"find stuff","parameters":{"type":"object"}}]}]
	}`)
	got := EstimateInputTokens(req)
	require.Greater(t, got, tokenestimate.Count("call tool"))
}
