package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
)

// resolveNewAPIMoonshotBaseURLOnSave ensures that newapi/Moonshot accounts
// created or updated via the admin UI have their base_url pinned to the
// region (api.moonshot.cn vs api.moonshot.ai) that actually accepts the
// supplied API key.
//
// Why this exists (Bug B): the moonshot regional probe helper has lived in
// internal/integration/newapi/moonshot_resolve_save.go since the fifth-platform
// design landed (docs/approved/admin-ui-newapi-platform-end-to-end.md), but
// it was only invoked from the admin "fetch upstream model list" flow
// (FetchUpstreamModelList in fetch_upstream_models.go). The CreateAccount /
// UpdateAccount save paths never ran it, so an admin who saved a Moonshot
// account with the default `https://api.moonshot.cn` base while holding an
// `.ai` (international) key would silently persist the wrong region. The
// relay hot path then deliberately does NOT do per-request 401 fallback
// (see moonshot_regional.go's package comment), so every relay request would
// fail with 401 until the operator noticed and re-saved the account.
//
// Behavior:
//   - No-op for non-newapi platforms or non-Moonshot channel types.
//   - No-op when the configured base_url is a custom reverse-proxy host (the
//     ShouldResolveMoonshotBaseURLAtSave gate respects user intent).
//   - On successful probe, overwrites credentials["base_url"] in place with
//     the winning region (no trailing slash) so accountRepo.Create/Update
//     persists the resolved value.
//   - On probe failure, returns the error so the admin sees a 400/500 with
//     "moonshot regional resolve: ..." rather than silently saving bad data.
//
// Callers must pass the live Account that is about to be persisted; the
// helper mutates account.Credentials in-place when didResolve is true.
func resolveNewAPIMoonshotBaseURLOnSave(ctx context.Context, account *Account) error {
	if account == nil || account.Credentials == nil {
		return nil
	}
	baseURL, _ := account.Credentials["base_url"].(string)
	apiKey, _ := account.Credentials["api_key"].(string)
	resolved, didResolve, err := newapifusion.MaybeResolveMoonshotBaseURLForNewAPI(
		ctx,
		account.Platform,
		account.ChannelType,
		baseURL,
		apiKey,
	)
	if err != nil {
		// Surface the underlying probe failure to the admin so they can fix
		// the api key or fall back to a custom base_url. Wrapping keeps the
		// error chain inspectable for logging without losing the upstream
		// status code text from moonshotProbeModelsOK.
		return fmt.Errorf("resolve moonshot region for newapi account: %w", err)
	}
	if !didResolve {
		return nil
	}
	resolved = strings.TrimRight(strings.TrimSpace(resolved), "/")
	if resolved == "" {
		return nil
	}
	account.Credentials["base_url"] = resolved
	slog.Info(
		"newapi_moonshot_base_url_resolved_on_save",
		"account_name", account.Name,
		"channel_type", account.ChannelType,
		"original_base_url", baseURL,
		"resolved_base_url", resolved,
	)
	return nil
}
