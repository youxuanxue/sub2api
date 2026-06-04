//go:build unit

package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestResolveOpenAIForwardDefaultMappedModel(t *testing.T) {
	groupWithDefault := &service.Group{DefaultMappedModel: "gpt-5.5"}
	apiKeyWithGroup := &service.APIKey{Group: groupWithDefault}

	tests := []struct {
		name          string
		apiKey        *service.APIKey
		fallbackModel string
		want          string
	}{
		{name: "explicit fallback wins over group default", apiKey: apiKeyWithGroup, fallbackModel: "gpt-5.4", want: "gpt-5.4"},
		{name: "empty fallback falls through to group default", apiKey: apiKeyWithGroup, fallbackModel: "", want: "gpt-5.5"},
		{name: "whitespace fallback falls through to group default", apiKey: apiKeyWithGroup, fallbackModel: "  ", want: "gpt-5.5"},
		{name: "nil api key returns empty", apiKey: nil, fallbackModel: "", want: ""},
		{name: "nil group returns empty", apiKey: &service.APIKey{}, fallbackModel: "", want: ""},
		{name: "group default trimmed", apiKey: &service.APIKey{Group: &service.Group{DefaultMappedModel: " gpt-5.5 "}}, fallbackModel: "", want: "gpt-5.5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveOpenAIForwardDefaultMappedModel(tt.apiKey, tt.fallbackModel); got != tt.want {
				t.Fatalf("resolveOpenAIForwardDefaultMappedModel() = %q, want %q", got, tt.want)
			}
		})
	}
}
