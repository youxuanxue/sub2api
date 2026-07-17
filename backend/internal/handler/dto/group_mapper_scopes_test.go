package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// TestGroupFromService_UserPathCarriesSupportedModelScopes locks the fix for the
// antigravity usage-guide gap: the user-facing keys DTO (GroupFromService /
// APIKeyFromService.Group) MUST surface supported_model_scopes so UseKeyModal can
// hide the Claude flavor for antigravity groups without claude scope. The field used to live
// only on AdminGroup, so the user path returned it as undefined and the guide always
// showed Claude.
func TestGroupFromService_UserPathCarriesSupportedModelScopes(t *testing.T) {
	src := &service.Group{
		ID:                   7,
		Name:                 "antigravity-no-claude-scope",
		Platform:             "antigravity",
		SupportedModelScopes: []string{"gemini_text", "gemini_image"},
	}

	// Direct user-path group mapper.
	g := GroupFromService(src)
	require.NotNil(t, g)
	require.Equal(t, []string{"gemini_text", "gemini_image"}, g.SupportedModelScopes)

	// Nested under a user API key (the shape KeysView actually consumes).
	key := APIKeyFromService(&service.APIKey{ID: 1, UserID: 2, Key: "sk-scopes", Group: src})
	require.NotNil(t, key.Group)
	require.Equal(t, []string{"gemini_text", "gemini_image"}, key.Group.SupportedModelScopes)

	// JSON must include the snake_case field so the frontend prop is populated.
	b, err := json.Marshal(key.Group)
	require.NoError(t, err)
	require.Contains(t, string(b), `"supported_model_scopes":["gemini_text","gemini_image"]`)
}

// TestGroupFromServiceAdmin_StillCarriesSupportedModelScopes guards that moving the
// field down to the embedded base Group did not drop it from the admin DTO (admin
// inherits it via embedding rather than an explicit field).
func TestGroupFromServiceAdmin_StillCarriesSupportedModelScopes(t *testing.T) {
	src := &service.Group{
		ID:                   8,
		Name:                 "antigravity-admin",
		Platform:             "antigravity",
		SupportedModelScopes: []string{"gemini_text", "gemini_image"},
	}

	ag := GroupFromServiceAdmin(src)
	require.NotNil(t, ag)
	require.Equal(t, []string{"gemini_text", "gemini_image"}, ag.SupportedModelScopes)

	b, err := json.Marshal(ag)
	require.NoError(t, err)
	require.Contains(t, string(b), `"supported_model_scopes":["gemini_text","gemini_image"]`)
}

// TestGroupFromService_EmptyScopesOmitted verifies the back-compat shape: a group
// with no scopes (unrestricted) omits the field, so the frontend gate treats it as
// "show all flavors" exactly as before #776.
func TestGroupFromService_EmptyScopesOmitted(t *testing.T) {
	g := GroupFromService(&service.Group{ID: 9, Name: "unrestricted", Platform: "antigravity"})
	require.NotNil(t, g)
	require.Empty(t, g.SupportedModelScopes)

	b, err := json.Marshal(g)
	require.NoError(t, err)
	require.NotContains(t, string(b), "supported_model_scopes")
}
