## 7. 附录：baseline JSON 速查与 stable accounts

- TLS canonical 模板真值源：`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` 的 `shared_baseline.tls_profile.name` **必须为** `tk_canonical_cc_oauth`（对照 `deploy/aws/stage0/tk_canonical_cc_oauth.json`）；guard-drift `generate_sql` 据此 upsert profile + 绑定 `tls_fingerprint_profile_id`。
- HTTP UA / mimicry 真值源：`deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（`cc_version` + `sonnet_opus` + `haiku`）；`sync-runtime` / `plan-http-mimicry-sync` 读它。
- tier baseline 数值仍存在于同一份 tiered JSON（`tier_order: l1..l5` 的 `baseline.account.*` / `baseline.extra.*`），但现由 **admin UI ApplyTier** 写、reconciler 自愈并发——本 skill 只在 check 比对时读，不写。
- 更新 stable_accounts 列表（仅人工确认后）：
  ```bash
  python3 ops/anthropic/check-edge-oauth-stability.py \
    --edge-id $EDGE --account-name $ACCT \
    --update-stable-list --confirm yes-update-anthropic-stable-list
  ```
  禁止在未确认稳定前更新 stable list。
