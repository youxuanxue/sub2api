package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildOpenAIV1SegmentURL(t *testing.T) {
	t.Parallel()
	require.Equal(t, "https://api.openai.com/v1/embeddings", buildOpenAIV1SegmentURL("", "embeddings"))
	require.Equal(t, "https://api.openai.com/v1/images/generations", buildOpenAIV1SegmentURL("", "images/generations"))
	require.Equal(t, "https://api.example.com/v1/embeddings", buildOpenAIV1SegmentURL("https://api.example.com/v1", "embeddings"))
	require.Equal(t, "https://api.example.com/v1/embeddings", buildOpenAIV1SegmentURL("https://api.example.com/v1/responses", "embeddings"))
}
