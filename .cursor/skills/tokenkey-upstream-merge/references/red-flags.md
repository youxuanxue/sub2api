## 7. Red flags

Stop and fix before PR if any is true:

- TokenKey-only code got added directly to `openai_gateway_service.go`, `openai_account_scheduler.go`, `gateway_bridge_dispatch.go`, `gateway.go`, or large admin Vue views without companion/facade/component extraction.
- A merged-in admin view under `frontend/src/views/admin/**` still wraps `<AppLayout>` (layout must come from the `AdminShellView` persistent shell), or an admin route was added inline in `router/index.ts` instead of `frontend/src/router/admin.tk.ts` — `scripts/checks/admin-shell-layout.py` (in preflight) flags the `<AppLayout>` regression mechanically.
- New endpoint lacks QA/trajectory capture or terminal semantics.
- Sensitive payload persists without redaction version contract.
- New upstream file/route/service was deleted or disabled without explicit regression justification.
- PR shape check would fail: no upstream merge commit, missing `upstream/main..HEAD`, or first-parent commit contains skip-ci markers.
- Direct `bridge.Dispatch*` call added outside the approved service boundary files (`gateway_bridge_dispatch.go` / `openai_gateway_bridge_dispatch*.go`) — engine dispatch eligibility must route through `engine.BuildDispatchPlan`; `engine-facade-sentinels.json` will flag this mechanically.
- New Gemini response path processes `internalThought`/`executableCode` blocks without calling `shouldDropGeminiInternalText` / `normalizeGeminiFunctionArgs` — thinking-block filter or tool-arg normalizer has drifted; `engine-facade-sentinels.json` `gemini_thinking_filter_*` entries will fail.
