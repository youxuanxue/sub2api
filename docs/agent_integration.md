# Agent Contract

Generated from live Gin route registrations in `backend/internal/server/routes`.
Do not hand-edit this file; run `python3 scripts/export_agent_contract.py`.

## HTTP Routes

Generated from live Gin route registrations; do not edit this section.

- `POST /alpha/search` from `backend/internal/server/routes/gateway.go`
- `GET /antigravity/models` from `backend/internal/server/routes/gateway.go`
- `POST /antigravity/v1/messages` from `backend/internal/server/routes/gateway.go`
- `POST /antigravity/v1/messages/count_tokens` from `backend/internal/server/routes/gateway.go`
- `GET /antigravity/v1/models` from `backend/internal/server/routes/gateway.go`
- `GET /antigravity/v1/usage` from `backend/internal/server/routes/gateway.go`
- `GET /antigravity/v1beta/models` from `backend/internal/server/routes/gateway.go`
- `POST /antigravity/v1beta/models/*modelAction` from `backend/internal/server/routes/gateway.go`
- `GET /antigravity/v1beta/models/:model` from `backend/internal/server/routes/gateway.go`
- `POST /api/event_logging/batch` from `backend/internal/server/routes/common.go`
- `GET /api/v1/admin/accounts` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/accounts/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/apply-oauth-credentials` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/apply-tier` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `POST /api/v1/admin/accounts/:id/clear-error` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/clear-rate-limit` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/duplicate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/models` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/models/sync-upstream` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/ollama-cloud-usage` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/:id/ollama-cloud-usage/auto-refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/ollama-cloud-usage/refresh` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/accounts/:id/ollama-cloud-usage/session` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/:id/ollama-cloud-usage/session` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/protocol-probe` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/recover-state` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/refresh-tier` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/reset-quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/revert-proxy-fallback` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/schedulable` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/scheduled-test-plans` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/set-privacy` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/shadow` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/stats` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/accounts/:id/temp-unschedulable` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/temp-unschedulable` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/today-stats` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/:id/upstream-billing-probe` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/:id/upstream-billing-probe` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/:id/usage` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/antigravity/default-model-mapping` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch-clear-error` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch-delete` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch-refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch-refresh-tier` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/batch-update-credentials` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/bulk-update` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/check-mixed-channel` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/cookie-auth` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/data` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/data` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/exchange-code` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/exchange-setup-token-code` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/generate-auth-url` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/generate-setup-token-url` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/import/antigravity-oauth` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/import/codex-session` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/model-mapping-presets` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/models/sync-upstream-preview` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/ollama-cloud-usage/settings` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/ollama-cloud-usage/settings` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/setup-token-cookie-auth` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/sync/crs` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/sync/crs/preview` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/today-stats/batch` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/upstream-billing-probe/batch` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/accounts/upstream-billing-probe/settings` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/accounts/upstream-billing-probe/settings` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/accounts/usage/batch` from `backend/internal/server/routes/admin_tk_account_usage_batch_routes.go`
- `GET /api/v1/admin/affiliates/invites` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/affiliates/rebates` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/affiliates/transfers` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/affiliates/users` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/affiliates/users/:user_id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/affiliates/users/:user_id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/affiliates/users/:user_id/overview` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/affiliates/users/batch-rate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/affiliates/users/lookup` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/announcements` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/announcements` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/announcements/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/announcements/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/announcements/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/announcements/:id/read-status` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/antigravity/oauth/auth-url` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/antigravity/oauth/exchange-code` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/antigravity/oauth/refresh-token` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/api-keys/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/audit-logs` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/audit-logs/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/audit-logs/clear` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/backups` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/backups/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups/:id/download-url` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/backups/:id/restore` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups/image-storage` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/backups/image-storage` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/backups/image-storage/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups/s3-config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/backups/s3-config` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/backups/s3-config/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/backups/schedule` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/backups/schedule` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-templates` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channel-monitor-templates` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/channel-monitor-templates/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-templates/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/channel-monitor-templates/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channel-monitor-templates/:id/apply` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-templates/:id/monitors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/channel-monitor-v2/config` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/dimensions` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/errors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/matrix` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/models` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/snapshot` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitor-v2/users` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitors` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channel-monitors` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/channel-monitors/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitors/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/channel-monitors/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channel-monitors/:id/duplicate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-monitors/:id/history` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channel-monitors/:id/run` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channel-type-models` from `backend/internal/server/routes/admin_tk_channel_routes.go`
- `GET /api/v1/admin/channel-types` from `backend/internal/server/routes/admin_tk_channel_routes.go`
- `POST /api/v1/admin/channel-types/fetch-upstream-models` from `backend/internal/server/routes/admin_tk_channel_routes.go`
- `GET /api/v1/admin/channels` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channels` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/channels/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channels/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/channels/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/channels/aggregated-group-models` from `backend/internal/server/routes/admin_tk_channel_routes.go`
- `GET /api/v1/admin/channels/model-pricing` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/channels/pricing/sync-models` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/cn-providers/accounts/:id/balance` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/cn-providers/accounts/:id/quota` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/compliance` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/compliance/accept` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/dashboard/aggregation/backfill` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/api-keys-trend` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/dashboard/api-keys-usage` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/groups` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/models` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/realtime` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/snapshot-v2` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/stats` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/trend` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/user-breakdown` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/users-ranking` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/dashboard/users-trend` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/dashboard/users-usage` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/agent/health` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/backups` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/backups` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/backups/:job_id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/data-management/config` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/s3/profiles` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/s3/profiles` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/data-management/s3/profiles/:profile_id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/data-management/s3/profiles/:profile_id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/s3/profiles/:profile_id/activate` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/s3/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/data-management/sources/:source_type/profiles` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/sources/:source_type/profiles` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/data-management/sources/:source_type/profiles/:profile_id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/data-management/sources/:source_type/profiles/:profile_id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/data-management/sources/:source_type/profiles/:profile_id/activate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/edge-accounts` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `POST /api/v1/admin/edge-accounts/:edge/accounts/:id/clear-rate-limit` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `POST /api/v1/admin/edge-accounts/:edge/accounts/:id/reset-quota` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `POST /api/v1/admin/edge-accounts/:edge/accounts/:id/schedulable` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `DELETE /api/v1/admin/edge-accounts/:edge/accounts/:id/temp-unschedulable` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `GET /api/v1/admin/edge-accounts/:edge/accounts/:id/usage` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `POST /api/v1/admin/edge-accounts/:edge/admin-session` from `backend/internal/server/routes/admin_tk_edge_accounts_routes.go`
- `GET /api/v1/admin/error-passthrough-rules` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/error-passthrough-rules` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/error-passthrough-rules/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/error-passthrough-rules/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/error-passthrough-rules/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/gemini/oauth/auth-url` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/gemini/oauth/capabilities` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/gemini/oauth/exchange-code` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/grok/accounts/:id/quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/accounts/:id/refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/accounts/:id/reset-quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/auth-url` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/grok/oauth/capabilities` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/create-from-oauth` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/exchange-code` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/password` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/reconcile` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/refresh-token` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/oauth/sso-token` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/grok/runtime-sanity` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/grok/sso-to-oauth` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/groups` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/groups/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/groups/:id` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/groups/:id/accounts` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/accounts` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/groups/:id/accounts` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/api-keys` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/composite-routes` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/groups/:id/composite-routes` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/groups/:id/composite-routes/:route_id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/groups/:id/composite-routes/:route_id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/groups/:id/composite-routes/preview` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/groups/:id/duplicate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/models-list-candidates` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/groups/:id/rate-multipliers` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/rate-multipliers` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/groups/:id/rate-multipliers` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/groups/:id/rpm-overrides` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/groups/:id/rpm-overrides` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/stats` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/:id/subscriptions` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/all` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/capacity-summary` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/live-capability` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/groups/sort-order` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/groups/usage-summary` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/openai/accounts/:id/quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/accounts/:id/quota/refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/accounts/:id/refresh` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/accounts/:id/reset-quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/create-from-codex-pat` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/create-from-oauth` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/exchange-code` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/generate-auth-url` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/openai/refresh-token` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/account-availability` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/advanced-settings` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/advanced-settings` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/alert-events` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/alert-events/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/alert-events/:id/status` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/alert-rules` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/ops/alert-rules` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/ops/alert-rules/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/alert-rules/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/ops/alert-silences` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/auth-cache-invalidation/health` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/concurrency` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/error-distribution` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/error-trend` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/failover-hop-stats` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/latency-histogram` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/openai-token-stats` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/overview` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/snapshot-v2` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/dashboard/throughput-trend` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/email-notification/config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/email-notification/config` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/errors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/errors/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/errors/:id/resolve` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/ingress-rejections` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/ingress-rejections/health` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/realtime-traffic` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/request-errors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/request-errors/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/request-errors/:id/resolve` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/request-errors/:id/upstream-errors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/requests` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/runtime/alert` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/runtime/alert` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/runtime/logging` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/runtime/logging` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/ops/runtime/logging/reset` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/settings/metric-thresholds` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/settings/metric-thresholds` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/system-logs` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/ops/system-logs/cleanup` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/system-logs/health` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/upstream-errors` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/upstream-errors/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/ops/upstream-errors/:id/resolve` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/user-concurrency` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/ops/ws/qps` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/payment/config` from `backend/internal/server/routes/payment.go`
- `PUT /api/v1/admin/payment/config` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/payment/dashboard` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/payment/orders` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/payment/orders/:id` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/orders/:id/cancel` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/orders/:id/refund` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/orders/:id/refund/query` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/orders/:id/retry` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/payment/plans` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/plans` from `backend/internal/server/routes/payment.go`
- `DELETE /api/v1/admin/payment/plans/:id` from `backend/internal/server/routes/payment.go`
- `PUT /api/v1/admin/payment/plans/:id` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/payment/providers` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/admin/payment/providers` from `backend/internal/server/routes/payment.go`
- `DELETE /api/v1/admin/payment/providers/:id` from `backend/internal/server/routes/payment.go`
- `PUT /api/v1/admin/payment/providers/:id` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/admin/promo-codes` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/promo-codes` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/promo-codes/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/promo-codes/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/promo-codes/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/promo-codes/:id/usages` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/prompt-audit/config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/prompt-audit/config` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/prompt-audit/endpoints/probe` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/prompt-audit/events` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/prompt-audit/events/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/prompt-audit/events/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/prompt-audit/events/batch-delete` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/prompt-audit/events/delete-by-filter` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/prompt-audit/events/delete-preview` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/prompt-audit/runtime` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/proxies/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/proxies/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies/:id/accounts` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies/:id/quality-check` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies/:id/stats` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies/:id/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies/all` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies/batch` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies/batch-delete` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/proxies/data` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/proxies/data` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/redeem-codes` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/redeem-codes/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/redeem-codes/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/redeem-codes/:id/expire` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/redeem-codes/batch-delete` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/redeem-codes/batch-update` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/redeem-codes/create-and-redeem` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/redeem-codes/export` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/redeem-codes/generate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/redeem-codes/stats` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/risk-control/api-keys/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/risk-control/config` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/risk-control/config` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/risk-control/hashes` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/risk-control/hashes/all` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/risk-control/logs` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/risk-control/status` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/risk-control/users/:user_id/unban` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/scheduled-test-plans` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/scheduled-test-plans/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/scheduled-test-plans/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/scheduled-test-plans/:id/results` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/settings/admin-api-key` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/admin-api-key` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/admin-api-key/regenerate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/beta-policy` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/beta-policy` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/email-template-preview` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/email-templates` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/email-templates/:event/:locale` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/email-templates/:event/:locale` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/email-templates/:event/:locale/restore-official` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/overload-cooldown` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/overload-cooldown` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/panel-rate-limit` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/panel-rate-limit` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/rate-limit-429-cooldown` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/rate-limit-429-cooldown` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/rectifier` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/rectifier` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/send-test-email` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/stream-timeout` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/stream-timeout` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/test-smtp` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/settings/web-search-emulation` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/settings/web-search-emulation` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/web-search-emulation/reset-usage` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/settings/web-search-emulation/test` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/subscriptions` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/subscriptions/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/subscriptions/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/:id/extend` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/subscriptions/:id/progress` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/:id/reset-quota` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/:id/restore` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/:id/revoke` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/assign` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/subscriptions/bulk-assign` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/supplier-sources` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/supplier-sources/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/supplier-sources/:id/discover` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources/:id/discover/jobs/:job_id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/supplier-sources/:id/probe` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources/:id/probe/jobs/:job_id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/supplier-sources/:id/sync` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/supplier-sources/:id/validate` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources/discover-channel-scoped-defaults` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/supplier-sources/priority-preview` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/system/check-updates` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/system/restart` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/system/rollback` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/system/rollback-versions` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/system/update` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/system/version` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/tiers` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `POST /api/v1/admin/tiers` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `DELETE /api/v1/admin/tiers/:id` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `GET /api/v1/admin/tiers/:id` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `PUT /api/v1/admin/tiers/:id` from `backend/internal/server/routes/admin_tk_tier_routes.go`
- `GET /api/v1/admin/tls-fingerprint-profiles` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/tls-fingerprint-profiles` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/tls-fingerprint-profiles/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/tls-fingerprint-profiles/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/tls-fingerprint-profiles/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/usage` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/usage/cleanup-tasks` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/usage/cleanup-tasks` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/usage/cleanup-tasks/:id/cancel` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/usage/search-api-keys` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/usage/search-users` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/usage/stats` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/user-attributes` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/user-attributes` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/user-attributes/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/user-attributes/:id` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/user-attributes/batch` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/user-attributes/reorder` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users` from `backend/internal/server/routes/admin.go`
- `DELETE /api/v1/admin/users/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/users/:id` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/api-keys` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/:id/api-keys` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/attributes` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/users/:id/attributes` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/:id/auth-identities` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/:id/balance` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/balance-history` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/platform-quotas` from `backend/internal/server/routes/admin.go`
- `PUT /api/v1/admin/users/:id/platform-quotas` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/:id/platform-quotas/reset` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/:id/replace-group` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/rpm-status` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/subscriptions` from `backend/internal/server/routes/admin.go`
- `GET /api/v1/admin/users/:id/usage` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/batch-concurrency` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/batch-limits` from `backend/internal/server/routes/admin.go`
- `POST /api/v1/admin/users/invite-trial` from `backend/internal/server/routes/admin_tk_invite_trial_routes.go`
- `GET /api/v1/admin/users/trial-presets` from `backend/internal/server/routes/admin_tk_invite_trial_routes.go`
- `PUT /api/v1/admin/users/trial-presets` from `backend/internal/server/routes/admin_tk_invite_trial_routes.go`
- `GET /api/v1/announcements` from `backend/internal/server/routes/user.go`
- `POST /api/v1/announcements/:id/read` from `backend/internal/server/routes/user.go`
- `POST /api/v1/auth/forgot-password` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/login` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/login/2fa` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/logout` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/me` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/bind-token` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/dingtalk/bind-login` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/dingtalk/bind/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/dingtalk/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/dingtalk/complete-registration` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/dingtalk/create-account` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/dingtalk/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/dingtalk/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/github/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/github/complete-registration` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/github/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/github/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/google/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/google/complete-registration` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/google/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/google/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/linuxdo/bind-login` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/linuxdo/bind/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/linuxdo/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/linuxdo/complete-registration` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/linuxdo/create-account` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/linuxdo/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/linuxdo/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/oidc/bind-login` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/oidc/bind/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/oidc/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/oidc/complete-registration` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/oidc/create-account` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/oidc/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/oidc/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/pending/bind-login` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/pending/create-account` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/pending/exchange` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/pending/send-verify-code` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/wechat/bind-login` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/wechat/bind/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/wechat/callback` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/wechat/complete-registration` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/wechat/create-account` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/wechat/payment/callback` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/wechat/payment/start` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/auth/oauth/wechat/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/oauth/wechat/start` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/passkey/login/begin` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/passkey/login/finish` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/refresh` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/register` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/reset-password` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/revoke-all-sessions` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/send-verify-code` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/validate-invitation-code` from `backend/internal/server/routes/auth.go`
- `POST /api/v1/auth/validate-promo-code` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/channel-monitor-v2/dimensions` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitor-v2/errors` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitor-v2/matrix` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitor-v2/models` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitor-v2/snapshot` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitor-v2/users` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitors` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channel-monitors/:id/status` from `backend/internal/server/routes/user.go`
- `GET /api/v1/channels/available` from `backend/internal/server/routes/user.go`
- `GET /api/v1/edge/accounts` from `backend/internal/server/routes/edge_tk_routes.go`
- `POST /api/v1/edge/accounts/:id/clear-rate-limit` from `backend/internal/server/routes/edge_tk_routes.go`
- `POST /api/v1/edge/accounts/:id/reset-quota` from `backend/internal/server/routes/edge_tk_routes.go`
- `POST /api/v1/edge/accounts/:id/schedulable` from `backend/internal/server/routes/edge_tk_routes.go`
- `DELETE /api/v1/edge/accounts/:id/temp-unschedulable` from `backend/internal/server/routes/edge_tk_routes.go`
- `GET /api/v1/edge/accounts/:id/usage` from `backend/internal/server/routes/edge_tk_routes.go`
- `POST /api/v1/edge/admin-session` from `backend/internal/server/routes/edge_tk_routes.go`
- `GET /api/v1/edge/scheduling-capacity` from `backend/internal/server/routes/edge_tk_routes.go`
- `GET /api/v1/groups/available` from `backend/internal/server/routes/user.go`
- `GET /api/v1/groups/rates` from `backend/internal/server/routes/user.go`
- `GET /api/v1/keys` from `backend/internal/server/routes/user.go`
- `POST /api/v1/keys` from `backend/internal/server/routes/user.go`
- `DELETE /api/v1/keys/:id` from `backend/internal/server/routes/user.go`
- `GET /api/v1/keys/:id` from `backend/internal/server/routes/user.go`
- `PUT /api/v1/keys/:id` from `backend/internal/server/routes/user.go`
- `GET /api/v1/me/api-keys/:id/capabilities` from `backend/internal/server/routes/user_tk_routes.go`
- `GET /api/v1/me/pricing-catalog` from `backend/internal/server/routes/user_tk_routes.go`
- `GET /api/v1/model-plaza` from `backend/internal/server/routes/model_plaza.go`
- `GET /api/v1/payment/checkout-info` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/config` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/limits` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/orders` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/orders/:id` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/orders/:id/cancel` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/orders/:id/refund-request` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/orders/my` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/orders/refund-eligible-providers` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/orders/verify` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/plans` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/public/orders/resolve` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/public/orders/verify` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/webhook/airwallex` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/webhook/alipay` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/payment/webhook/easypay` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/webhook/easypay` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/webhook/stripe` from `backend/internal/server/routes/payment.go`
- `POST /api/v1/payment/webhook/wxpay` from `backend/internal/server/routes/payment.go`
- `GET /api/v1/public/pricing` from `backend/internal/server/routes/public_tk_routes.go`
- `POST /api/v1/redeem` from `backend/internal/server/routes/user.go`
- `GET /api/v1/redeem/history` from `backend/internal/server/routes/user.go`
- `GET /api/v1/settings/email-unsubscribe` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/settings/public` from `backend/internal/server/routes/auth.go`
- `GET /api/v1/subscriptions` from `backend/internal/server/routes/user.go`
- `GET /api/v1/subscriptions/active` from `backend/internal/server/routes/user.go`
- `GET /api/v1/subscriptions/progress` from `backend/internal/server/routes/user.go`
- `GET /api/v1/subscriptions/summary` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/:id` from `backend/internal/server/routes/user.go`
- `POST /api/v1/usage/dashboard/api-keys-usage` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/dashboard/models` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/dashboard/snapshot-v2` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/dashboard/stats` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/dashboard/trend` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/errors` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/errors/:id` from `backend/internal/server/routes/user.go`
- `GET /api/v1/usage/stats` from `backend/internal/server/routes/user.go`
- `PUT /api/v1/user` from `backend/internal/server/routes/user.go`
- `DELETE /api/v1/user/account-bindings/:provider` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/account-bindings/email` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/account-bindings/email/send-code` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/aff` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/aff/transfer` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/api-keys/:id/usage/daily` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/auth-identities/bind/start` from `backend/internal/server/routes/user.go`
- `DELETE /api/v1/user/notify-email` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/notify-email/send-code` from `backend/internal/server/routes/user.go`
- `PUT /api/v1/user/notify-email/toggle` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/notify-email/verify` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/onboarding-tour-completed` from `backend/internal/server/routes/user_tk_routes.go`
- `GET /api/v1/user/passkeys` from `backend/internal/server/routes/user.go`
- `DELETE /api/v1/user/passkeys/:id` from `backend/internal/server/routes/user.go`
- `PATCH /api/v1/user/passkeys/:id` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/passkeys/register/begin` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/passkeys/register/finish` from `backend/internal/server/routes/user.go`
- `PUT /api/v1/user/password` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/platform-quotas` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/profile` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/totp/disable` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/totp/enable` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/totp/send-code` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/totp/setup` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/totp/status` from `backend/internal/server/routes/user.go`
- `POST /api/v1/user/totp/step-up` from `backend/internal/server/routes/user.go`
- `GET /api/v1/user/totp/verification-method` from `backend/internal/server/routes/user.go`
- `GET /api/v1/users/me/qa/bundle-exports/:job_id` from `backend/internal/server/routes/user_tk_routes.go`
- `POST /api/v1/users/me/qa/bundles` from `backend/internal/server/routes/user_tk_routes.go`
- `GET /api/v1/users/me/qa/bundles/:job_id` from `backend/internal/server/routes/user_tk_routes.go`
- `POST /api/v1/users/me/qa/bundles/:job_id/export` from `backend/internal/server/routes/user_tk_routes.go`
- `GET /backend-api/codex/:call_id` from `backend/internal/server/routes/gateway.go`
- `POST /backend-api/codex/alpha/search` from `backend/internal/server/routes/gateway.go`
- `GET /backend-api/codex/models` from `backend/internal/server/routes/gateway.go`
- `POST /backend-api/codex/realtime/calls` from `backend/internal/server/routes/gateway.go`
- `GET /backend-api/codex/responses` from `backend/internal/server/routes/gateway.go`
- `POST /backend-api/codex/responses` from `backend/internal/server/routes/gateway.go`
- `POST /backend-api/codex/responses/*subpath` from `backend/internal/server/routes/gateway.go`
- `POST /chat/completions` from `backend/internal/server/routes/gateway.go`
- `GET /custom-voices` from `backend/internal/server/routes/gateway.go`
- `POST /custom-voices` from `backend/internal/server/routes/gateway.go`
- `DELETE /custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `GET /custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `PATCH /custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `GET /custom-voices/:voice_id/audio` from `backend/internal/server/routes/gateway.go`
- `POST /embeddings` from `backend/internal/server/routes/gateway.go`
- `GET /health` from `backend/internal/server/routes/common.go`
- `GET /health/inflight` from `backend/internal/server/routes/common.go`
- `GET /health/live` from `backend/internal/server/routes/common.go`
- `POST /images/edits` from `backend/internal/server/routes/gateway.go`
- `POST /images/edits/async` from `backend/internal/server/routes/gateway.go`
- `POST /images/generations` from `backend/internal/server/routes/gateway.go`
- `POST /images/generations/async` from `backend/internal/server/routes/gateway.go`
- `POST /images/presign` from `backend/internal/server/routes/gateway.go`
- `GET /images/tasks/:task_id` from `backend/internal/server/routes/gateway.go`
- `POST /messages/count_tokens` from `backend/internal/server/routes/gateway.go`
- `GET /models` from `backend/internal/server/routes/gateway.go`
- `POST /openrouter/v1/images` from `backend/internal/server/routes/gateway.go`
- `GET /openrouter/v1/models` from `backend/internal/server/routes/gateway.go`
- `POST /openrouter/v1/videos` from `backend/internal/server/routes/gateway.go`
- `GET /openrouter/v1/videos/:id` from `backend/internal/server/routes/gateway.go`
- `GET /realtime` from `backend/internal/server/routes/gateway.go`
- `GET /responses` from `backend/internal/server/routes/gateway.go`
- `POST /responses` from `backend/internal/server/routes/gateway.go`
- `POST /responses/*subpath` from `backend/internal/server/routes/gateway.go`
- `GET /setup/status` from `backend/internal/server/routes/common.go`
- `POST /stt` from `backend/internal/server/routes/gateway.go`
- `POST /tts` from `backend/internal/server/routes/gateway.go`
- `POST /v1/alpha/search` from `backend/internal/server/routes/gateway.go`
- `POST /v1/chat/completions` from `backend/internal/server/routes/gateway.go`
- `GET /v1/custom-voices` from `backend/internal/server/routes/gateway.go`
- `POST /v1/custom-voices` from `backend/internal/server/routes/gateway.go`
- `DELETE /v1/custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `GET /v1/custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `PATCH /v1/custom-voices/:voice_id` from `backend/internal/server/routes/gateway.go`
- `GET /v1/custom-voices/:voice_id/audio` from `backend/internal/server/routes/gateway.go`
- `POST /v1/embeddings` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/batches` from `backend/internal/server/routes/gateway.go`
- `DELETE /v1/images/batches/:id` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches/:id` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/batches/:id/cancel` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches/:id/download` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches/:id/items` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches/:id/items/:custom_id/content` from `backend/internal/server/routes/gateway.go`
- `DELETE /v1/images/batches/:id/outputs` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/batches/models` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/edits` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/edits/async` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/generations` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/generations/async` from `backend/internal/server/routes/gateway.go`
- `POST /v1/images/presign` from `backend/internal/server/routes/gateway.go`
- `GET /v1/images/tasks/:task_id` from `backend/internal/server/routes/gateway.go`
- `POST /v1/live` from `backend/internal/server/routes/gateway.go`
- `GET /v1/live/:call_id` from `backend/internal/server/routes/gateway.go`
- `POST /v1/messages` from `backend/internal/server/routes/gateway.go`
- `POST /v1/messages/count_tokens` from `backend/internal/server/routes/gateway.go`
- `GET /v1/models` from `backend/internal/server/routes/gateway.go`
- `GET /v1/realtime` from `backend/internal/server/routes/gateway.go`
- `GET /v1/responses` from `backend/internal/server/routes/gateway.go`
- `POST /v1/responses` from `backend/internal/server/routes/gateway.go`
- `POST /v1/responses/*subpath` from `backend/internal/server/routes/gateway.go`
- `POST /v1/stt` from `backend/internal/server/routes/gateway.go`
- `GET /v1/sub2api/billing` from `backend/internal/server/routes/gateway.go`
- `POST /v1/tts` from `backend/internal/server/routes/gateway.go`
- `GET /v1/usage` from `backend/internal/server/routes/gateway.go`
- `POST /v1/video/generations` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/video/generations/:task_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /v1/videos` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/:task_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/:task_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /v1/videos/edits` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/edits/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/edits/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /v1/videos/extensions` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/extensions/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/extensions/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /v1/videos/generations` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/generations/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /v1/videos/generations/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /v1/web_search` from `backend/internal/server/routes/gateway.go`
- `POST /v1/x_search` from `backend/internal/server/routes/gateway.go`
- `GET /v1beta/models` from `backend/internal/server/routes/gateway.go`
- `POST /v1beta/models/*modelAction` from `backend/internal/server/routes/gateway.go`
- `GET /v1beta/models/:model` from `backend/internal/server/routes/gateway.go`
- `POST /video/generations` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /video/generations/:task_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /videos` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/:task_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/:task_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /videos/edits` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/edits/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/edits/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /videos/extensions` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/extensions/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/extensions/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /videos/generations` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/generations/:request_id` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `GET /videos/generations/:request_id/content` from `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`
- `POST /web_search` from `backend/internal/server/routes/gateway.go`
- `POST /x_search` from `backend/internal/server/routes/gateway.go`

## CLI

Generated from the live argparse parser factories; do not edit this section.

### `python3 ops/pricing/modelops.py`

Root options:
- `--selftest`: run offline selftest

#### `modelops.py plan`

build a read-only model operations plan

- `--account-id`: default account id for --upstream PATH
- `--upstream` (repeatable): ACCOUNT:PATH or PATH (with --account-id). JSON list/object or newline model list.
- `--candidate` (repeatable): ACCOUNT:MODEL ad hoc candidate; can repeat
- `--probe-results` (repeatable): TSV output from ops/pricing/probe-servable-models.sh; can repeat
- `--live-mapping`: JSON snapshot of live account model_mapping
- `--format` (choices: `text`, `json`; default: `text`):

#### `modelops.py activate`

validate independent evidence, plan prod mapping, and explicitly activate a model-surface bundle

- `--bundle` (default: `ops/pricing/model-surface-bundle.json`): target generated model-surface bundle
- `--current-bundle` (required): currently active model-surface bundle
- `--probe-evidence` (required): fresh independent model_activation_probe JSON
- `--pricing-evidence` (required): fresh independent model_activation_pricing JSON
- `--prod-instance-id`: pin the full prod activation chain to this EC2 instance id
- `--confirm`: write confirmation phrase: yes-activate-model-surface
- `--format` (choices: `text`, `json`; default: `text`):

### `python3 ops/pricing/manage-account-model-mapping-runtime.py`

Root options:
- `--selftest`:

#### `manage-account-model-mapping-runtime.py validate`

validate and print canonical JSON

- `--file` (required):
- `--bundle`: generated model-surface bundle to validate against

#### `manage-account-model-mapping-runtime.py check`

compare runtime settings to a JSON file

- `--file` (required):
- `--target` (default: `all-deployable-and-prod`): prod, edge:<id>, or all-deployable-and-prod
- `--json`: machine-readable output
- `--parallel` (default: `6`): parallel SSM workers

#### `manage-account-model-mapping-runtime.py check-accounts`

post-release read-only account model_mapping SSOT diff (prod only by default)

- `--json`: machine-readable output
- `--skip-prod`: check deployable edges only
- `--include-edges`: also check deployable edges (default: prod only)
- `--prod-instance-id`: use this prod EC2 instance id instead of resolving the prod stack
- `--bundle`: generated model-surface bundle to check against
- `--parallel` (default: `6`): parallel SSM workers

#### `manage-account-model-mapping-runtime.py release-gate`

explicit model-activation check: prod account model_mapping must cover the selected bundle floor

- `--json`: machine-readable output
- `--prod-instance-id`: use this prod EC2 instance id instead of resolving the prod stack
- `--bundle`: generated model-surface bundle to check against
- `--parallel` (default: `6`): parallel SSM workers

#### `manage-account-model-mapping-runtime.py apply-accounts`

explicitly apply reviewed SSOT diffs to live accounts

- `--target` (required): prod, edge:<id>, or all-deployable-and-prod
- `--account-ids`: comma-separated positive account IDs for targeted apply (single explicit target only)
- `--expected-plan-sha256`: required in targeted mode for non-dry-run; must match recomputed plan digest
- `--confirm`: required for writes: yes-apply-account-model-mapping
- `--dry-run`: print the planned account/group changes without writing
- `--prod-instance-id`: pin prod planning and apply to this EC2 instance id
- `--bundle`: generated model-surface bundle to apply
- `--parallel` (default: `3`): parallel SSM workers

#### `manage-account-model-mapping-runtime.py assign-vertex-profiles`

guardedly assign ch41 capability profiles on prod without changing model_mapping

- `--file` (required):
- `--confirm`: required for writes: yes-assign-vertex-capability-profiles
- `--dry-run`: verify selectors and print profile-only changes
- `--prod-instance-id`: pin prod planning and apply to this EC2 instance id
- `--bundle`: generated model-surface bundle whose profile names are allowed

#### `manage-account-model-mapping-runtime.py sync-runtime`

plan or explicitly write one target runtime setting

- `--file` (required):
- `--target` (required): one target: prod or edge:<id>
- `--confirm`: write instead of dry-run: yes-change-account-model-mapping-runtime
- `--bundle`: generated model-surface bundle to validate against

#### `manage-account-model-mapping-runtime.py clear-runtime`

plan or explicitly delete one target runtime setting

- `--target` (required): one target: prod or edge:<id>
- `--confirm`: write instead of dry-run: yes-change-account-model-mapping-runtime

#### `manage-account-model-mapping-runtime.py example`

print an example runtime JSON

### `python3 ops/archive/data_layer_archive_cleanup_hold.py`

#### `data_layer_archive_cleanup_hold.py plan`

read the current production cleanup state

#### `data_layer_archive_cleanup_hold.py apply`

disable cleanup and write a receipt

- `--receipt` (required):
- `--confirm` (required):

#### `data_layer_archive_cleanup_hold.py verify`

verify an existing hold receipt

- `--receipt` (required):

#### `data_layer_archive_cleanup_hold.py release`

restore the pre-hold cleanup state

- `--receipt` (required):
- `--activation-plan` (required):
- `--output`: optional path to persist the release receipt
- `--confirm` (required):

### `python3 ops/archive/data_layer_archive_prod_export.py`

#### `data_layer_archive_prod_export.py plan`

offline export plan

- `--table` (required; choices: `ops_system_logs`, `ops_error_logs`):
- `--export-scope` (choices: `legacy_cold`, `post_legacy_cold`; default: `legacy_cold`):
- `--legacy-upper-exclusive` (default: `2026-07-01T00:00:00.000000Z`): exclusive upper created_at bound for legacy scope
- `--legacy-lower-inclusive` (default: `2026-07-01T00:00:00.000000Z`): inclusive lower created_at bound for post-legacy scope
- `--timeout-seconds` (default: `120`):
- `--max-rows` (default: `50000`):
- `--max-logical-bytes` (default: `268435456`):

#### `data_layer_archive_prod_export.py init-ledger`

create a continuation ledger

- `--ledger` (required):
- `--table` (required; choices: `ops_system_logs`, `ops_error_logs`):
- `--export-scope` (choices: `legacy_cold`, `post_legacy_cold`; default: `legacy_cold`):
- `--legacy-upper-exclusive` (default: `2026-07-01T00:00:00.000000Z`):
- `--legacy-lower-inclusive` (default: `2026-07-01T00:00:00.000000Z`):

#### `data_layer_archive_prod_export.py run-batch`

export one cold batch from ledger scope

- `--ledger` (required):
- `--evidence-root` (required):
- `--cleanup-hold-receipt` (required):
- `--ssm-timeout-seconds` (default: `900`):
- `--confirm` (required):
- `--timeout-seconds` (default: `120`):
- `--max-rows` (default: `50000`):
- `--max-logical-bytes` (default: `268435456`):
- `--verify-restore`:
- `--restore-target-dsn` (default: ``):
- `--seed`:

### `python3 ops/archive/data_layer_archive_promote_batch.py`

#### `data_layer_archive_promote_batch.py plan`

offline promote plan

- `--batch-id` (required):

#### `data_layer_archive_promote_batch.py promote`

promote one export batch

- `--batch-id` (required):
- `--confirm` (required):

#### `data_layer_archive_promote_batch.py promote-ledger`

promote all batches listed in an export ledger

- `--export-ledger` (required):
- `--promote-ledger` (required):
- `--confirm` (required):

#### `data_layer_archive_promote_batch.py init-promote-ledger`

create promote ledger

- `--promote-ledger` (required):

### `python3 ops/archive/data_layer_archive_closeout.py`

Root options:
- `--export-ledger` (required):
- `--promote-ledger` (required):
- `--cleanup-hold-receipt` (required):
- `--closeout-receipt` (required):
- `--evidence-root` (required):
- `--restore-target-dsn` (required):
- `--seed` (required):
- `--confirm` (required):

### `python3 ops/migration/usage_logs_daily_partition.py`

#### `usage_logs_daily_partition.py status`



- `--target` (required):

#### `usage_logs_daily_partition.py prepare`



- `--target` (required):
- `--receipt` (required):
- `--confirm` (required):

#### `usage_logs_daily_partition.py abort`



- `--target` (required):
- `--receipt` (required):
- `--legacy-upper-exclusive` (required):
- `--confirm` (required):

#### `usage_logs_daily_partition.py cutover`



- `--target` (required):
- `--prepare-receipt` (required):
- `--cutover-receipt` (required):
- `--confirm` (required):

#### `usage_logs_daily_partition.py verify`



- `--target` (required):
- `--prepare-receipt` (required):

## MCP

_No MCP entrypoint detected in this repository._

---

# Agent Contract Notes

## TokenKey first-class platforms (account / group `platform` field)

The gateway routes above dispatch to TokenKey first-class platforms.
The canonical names are defined in `backend/internal/domain/constants.go`
(`PlatformOpenAI`, `PlatformAnthropic`, `PlatformGemini`,
`PlatformAntigravity`, `PlatformNewAPI`, `PlatformKiro`, `PlatformGrok`).
Every TokenKey account and
group MUST set `platform` to exactly one of:

| Platform name | Gateway entry points (subset) | Notes |
|---|---|---|
| `openai` | `POST /v1/chat/completions`, `POST /v1/messages`, `POST /v1/responses`, `GET /v1/responses` (WS), `POST /v1/embeddings`, `POST /v1/images/generations`, `GET /v1/usage`, `GET /v1/models` | OpenAI-compat surface — also accepts `/v1/messages` (Anthropic-shaped) and `/v1/responses` (OpenAI Responses API). |
| `anthropic` | `POST /v1/messages` (native Anthropic), `POST /v1/messages/count_tokens`, `POST /responses`, `GET /responses` (WS), and the `requireGroupAnthropic`-gated `/responses`, `/chat/completions`, `/embeddings`, `/images/generations` | Native Claude account pool. |
| `gemini` | `GET /v1beta/models`, `GET /v1beta/models/:model`, `POST /v1beta/models/*modelAction` | Gemini-native surface. |
| `antigravity` | `GET /antigravity/models`, the `/antigravity/v1` and `/antigravity/v1beta` subtrees | Antigravity-native surface; admin endpoints under `/admin/antigravity/*`. |
| `newapi` | Same OpenAI-compat surface as `openai` (`/v1/chat/completions`, `/v1/messages`, `/v1/responses` and the WS variant) | First-class fifth platform — see next section. |
| `kiro` | `POST /v1/messages` through the Anthropic-shaped client surface | Kiro-native scheduling pool backed by the vendored CodeWhisperer/EventStream protocol layer. |
| `grok` | Same OpenAI-compat surface as `openai` (`/v1/chat/completions`, `/v1/messages`, `/v1/responses`, embeddings/images/video where enabled) | First-class Grok/xAI platform. Edge capacity is `type=oauth`; prod edge relay stubs may be `type=apikey` with an edge `base_url`. |

## Admin account model options

`GET /admin/accounts/:id/models` returns one cross-platform response shape for
all account platforms. Each `data` item contains exactly `id` and
`display_name`; platform-specific model metadata is not part of this admin API.

## NewAPI as first-class fifth platform

Per `docs/approved/newapi-as-fifth-platform.md`, `group.platform = "newapi"`
participates in the **OpenAI-compatible** scheduling pool and answers the
same three OpenAI-shaped entry points (`/v1/chat/completions`,
`/v1/messages`, `/v1/responses`) as `openai` groups. Agent-visible
contract:

- A `newapi` group MUST contain at least one `Account.Platform = "newapi"`
  whose `channel_type > 0`. Any account with `channel_type = 0` is
  filtered out of the scheduling pool (bridge dispatch needs a channel
  target). An empty pool surfaces a clear `no available openai accounts`
  / `no available accounts` error — the gateway NEVER falls back across
  platforms.
- An `openai` group continues to receive only `openai` accounts; an
  `openai` account must never be dispatched to a `newapi` group and
  vice versa. The strict-equality filter is `IsOpenAICompatPoolMember`
  in `backend/internal/service/account_tk_compat_pool.go`; a stale
  sticky-session binding pointing at the wrong-platform account is
  invalidated and the request fails over to load-balance.
- `messages_dispatch_model_config` (the per-group model-name remap used
  for `/v1/messages` translation) is **preserved** for both `openai` and
  `newapi` groups and **cleared** for `anthropic` / `gemini` /
  `antigravity` groups. The shared predicate is
  `isOpenAICompatPlatformGroup` in
  `backend/internal/service/openai_messages_dispatch_tk_newapi.go`.

### Image and video generation (Volcengine and other newapi channels)

`newapi` (and `openai`) groups expose two extra OpenAI-compat surfaces
that route through the `bridge` package and the upstream new-api
adapter registry:

- **Image generation** — `POST /v1/images/generations` (and the
  no-prefix alias `POST /images/generations`). Synchronous: returns the
  upstream JSON body inline. Volcengine `channel_type = 45` (e.g.
  `doubao-seedream-4-0-250828`) is supported via the upstream `volcengine`
  adapter.
- **Video generation** — `POST /v1/video/generations`,
  `GET /v1/video/generations/:task_id`, and the OpenAI-compat aliases
  `POST /v1/videos`, `GET /v1/videos/:task_id` (plus their no-prefix
  variants). Asynchronous: the POST returns a TokenKey-issued
  `task_id` (prefix `vt_`); the GET polls upstream for status. Routing
  is pinned at submit time and stored in the `service.VideoTaskCache` (Redis
  primary, in-memory fallback for single-replica dev). Currently
  supported channel types: `45` (VolcEngine — Doubao Seedance) and
  `54` (DoubaoVideo). The set is auto-derived from
  `relay.GetTaskAdaptor` so any new task adaptor merged from upstream
  new-api lights up automatically once the channel type appears in
  `IsVideoSupportedChannelType`.

The video registry record TTL defaults to 24h. Polls after expiry or
after a terminal status (`succeeded` / `failed`) return 404.

## OpenRouter provider seller surface (TokenKey)

TokenKey may expose a seller catalog for OpenRouter onboarding:

- `GET /openrouter/v1/models` — allowlisted OR API keys only; returns
  OpenRouter provider **schema 2.4** documents with `tokenkey/<model>` ids
  and modality-owned USD prices after group/user rate multipliers.
  Configured via ops JSON setting `tk_openrouter_provider_config`
  (`group_ids`, `billing_user_id`, `allowed_api_key_ids`,
  `monitor_api_key_ids`). Customer gateway paths outside this seller
  surface are unchanged.
- Allowlisted OR/monitor keys also receive the same catalog from
  `GET /v1/models`.
- Inference keys accept catalog ids on `POST /v1/chat/completions`; TokenKey
  rewrites `tokenkey/<model>` back to internal scheduling ids before routing.
- **Scheme C loop guard**: public groups (`is_exclusive=false`) must not
  bind aggregator upstream accounts (`channel_type` 20/49/53 or
  `openrouter.ai` base URLs). ct20 OpenRouter upstream stays on
  exclusive/internal groups only.

## OpenAI-compatible entry-point families

The same handler set under `backend/internal/server/routes/gateway.go`
(via `tkOpenAICompatChatCompletionsPOST`,
`tkOpenAICompatMessagesPOST`, `tkOpenAICompatResponsesPOST`,
`tkOpenAICompatCountTokensPOST`, `tkOpenAICompatEmbeddingsHandler`,
`tkOpenAICompatImageGenerationsHandler`, `tkOpenAICompatVideoSubmitHandler`,
`tkOpenAICompatVideoFetchHandler`) serves both `openai` and
`newapi` groups. Agents that key behavior off `group.platform` should
treat these two values as the OpenAI-compatible class; everything else
is platform-native.

## Drift checks

The contract guards live in:

- `scripts/export_agent_contract.py --check` — Notes-section coverage
  (every first-class platform must be acknowledged here) plus a
  route-count sanity check.
- `scripts/preflight.sh § 2` — `IsOpenAICompatPoolMember` /
  `OpenAICompatPlatforms` adoption (forbids regression to bare
  `!account.IsOpenAI()` or direct `PlatformOpenAI` bucket fetches).
