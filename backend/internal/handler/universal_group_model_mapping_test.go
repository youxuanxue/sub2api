package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestGroupModelMappingRoutingMode(t *testing.T) {
	for _, mode := range []string{"", service.RoutingModeDirect, service.RoutingModeUniversal} {
		for _, configured := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/defaults", true: "/configured"}[configured], func(t *testing.T) {
				group := &service.Group{ID: 2, Platform: service.PlatformOpenAI, AllowMessagesDispatch: true}
				if configured {
					group.MessagesDispatchModelConfig = service.OpenAIMessagesDispatchModelConfig{
						OpusMappedModel:    "gpt-5.4",
						ExactModelMappings: map[string]string{"claude-opus-4-6": "gpt-5.5"},
					}
				}
				key := &service.APIKey{RoutingMode: mode, Group: group, GroupID: &group.ID}
				want := group.ResolveMessagesDispatchModel("claude-opus-4-6")
				if key.IsUniversal() {
					want = ""
				}
				mapped := resolveOpenAIMessagesDispatchMappedModel(key, "claude-opus-4-6")
				require.Equal(t, want, mapped)
				_, canFallback := tkOpenAIDispatchSelectionFallbackModel(mapped, "claude-opus-4-6", service.ErrUnsupportedModel)
				require.Equal(t, !key.IsUniversal(), canFallback, "selection and token counting must not borrow a billing-group fallback")
				body := []byte(`{"model":"claude-opus-4-6","input":"hello"}`)
				out := tkResponsesForwardDispatchBody(key, &service.Account{Type: service.AccountTypeOAuth}, body, nil, service.ReplaceModelInBody)
				wantModel := want
				if wantModel == "" {
					wantModel = "claude-opus-4-6"
				}
				require.Equal(t, wantModel, gjson.GetBytes(out, "model").String())
				require.Same(t, group, key.Group, "billing must retain its full group")
				require.True(t, group.AllowMessagesDispatch, "mapping policy must not mutate endpoint permission")
				require.NotEmpty(t, resolveOpenAIMessagesDispatchMappedModel(&service.APIKey{Group: group}, "claude-opus-4-6"), "a shared group must keep Direct mappings")
			})
		}
	}
}

func TestUniversalGroupDefaultModelCannotReenterThroughFallback(t *testing.T) {
	group := &service.Group{ID: 2, DefaultMappedModel: "gpt-5.5"}
	for _, mode := range []string{service.RoutingModeDirect, service.RoutingModeUniversal} {
		key := &service.APIKey{RoutingMode: mode, Group: group, GroupID: &group.ID}
		for _, fallback := range []string{"", "gpt-5.4"} {
			want := "gpt-5.5"
			if fallback != "" {
				want = fallback
			}
			if key.IsUniversal() {
				want = ""
			}
			require.Equal(t, want, resolveOpenAIForwardDefaultMappedModel(key, fallback))
		}
	}
}
