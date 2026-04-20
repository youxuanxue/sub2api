---
title: Admin UI — Fifth Platform `newapi` End-to-End Visibility & Operability
status: pending
approved_by: pending
approved_at: pending
authors: [agent]
created: 2026-04-20
related_prs: []
related_commits: []
related_stories: [US-017]
related_audit: tester report 2026-04-20 — 创建分组 modal lacks fifth platform option
supersedes: none
parent_design: docs/approved/newapi-as-fifth-platform.md
---

# Admin UI — Fifth Platform `newapi` End-to-End

## 0. TL;DR

`docs/approved/newapi-as-fifth-platform.md` (shipped v1.4.0) explicitly **deferred admin-UI
integration** ("frontend：`platformOptions` 是否含 newapi 由 admin UI 决定，不在本 design
范围"). Result today: backend, scheduler, sticky routing, error passthrough, bridge—every
runtime path treats `newapi` as a first-class fifth platform; **but the admin UI does not
expose it**. Operators cannot create newapi groups or accounts through the UI; the only
workaround is hand-crafting admin API calls.

This design closes that gap with the **smallest** UI surface that lets an operator drive
newapi end-to-end (create group → create account → see correctly-labelled account → filter
list). Out-of-scope polish (ops-dashboard filter, error-passthrough rules, bulk edit,
gradient/discount/button color variants) is enumerated for stage-3 follow-up but explicitly
excluded from the prototype.

## 1. Scope

### In-scope (this design + prototype)

1. **Single source of truth for platform options** — extract `usePlatformOptions()`
   composable backed by `frontend/src/constants/gatewayPlatforms.ts` `GATEWAY_PLATFORMS`
   (already includes `newapi`). Replace `GroupsView.vue`'s two hardcoded option lists.
2. **Account creation** — `CreateAccountModal.vue` gains a 5th platform segment
   `newapi` that wires the existing-but-unused `AccountNewApiPlatformFields.vue` to the
   existing-but-unused `listChannelTypes()` / `fetchUpstreamModels()` API clients.
3. **Account display correctness** — `PlatformTypeBadge.vue` adds a `newapi` arm and
   stops using "Gemini" as the catch-all fallback. (Today a newapi account renders as
   "Gemini" + blue badge, which is silently wrong data display, not a styling nit.)
4. **Regression safeguard** — vitest unit covering `usePlatformOptions()` returns 5
   platforms in canonical order so future refactors cannot drop newapi again.

### Out-of-scope (stage-3 backlog, listed in `docs/task-breakdown-admin-ui-newapi.md`)

- `AccountTableFilters.vue` / `OpsDashboardHeader.vue` / `ErrorPassthroughRulesModal.vue`
  platform pickers — same composable swap, but each has its own filter semantics that
  warrant individual review.
- `EditAccountModal.vue` / `BulkEditAccountModal.vue` — must not regress, but full
  newapi-channel editing in bulk has UX implications (mass channel_type change is
  destructive). Keep behind a separate review.
- `utils/platformColors.ts` — extend `Platform` union and add `newapi` to all 9
  variant maps for visual completeness in non-badge surfaces.
- `PlatformIcon.vue` — picking a brand mark for newapi is a design decision, not a
  bug. Today's generic-globe fallback is acceptable.
- `SubscriptionsView.vue` — newapi has no OAuth subscription concept. Adding it would
  mislead.

### Non-goals (will not do, and design explains why)

- **No new backend endpoints.** Backend already accepts `Platform: "newapi"` in
  `CreateGroup` / `CreateAccount` (admin_service.go:1565 enforces `channel_type > 0`).
  No backend change needed; doing one would violate CLAUDE.md §5 minimal API surface.
- **No new DTO fields.** `AccountNewApiPlatformFields` already binds to existing
  `channel_type` / `base_url` / `api_key` — they round-trip via existing APIs.
- **No global UI restructure.** Per CLAUDE.md §5.x, prefer additive injection points
  over rewriting upstream-shaped files (`CreateAccountModal.vue` is upstream-derived).
  All edits are append-only inside existing `v-if` chains.

## 2. Current Failure Path

```
User → 「分组管理」→ 创建分组
   → modal 平台下拉只渲染 [Anthropic, OpenAI, Gemini, Antigravity]
   → 用户无法选 newapi → 只能放弃或操作 admin API
```

Root cause: `frontend/src/views/admin/GroupsView.vue:2813-2818` hardcodes a 4-element
`platformOptions` literal. Same anti-pattern repeats in `:2820-2826` (filter), and in 4
other admin views (`AccountTableFilters.vue`, `OpsDashboardHeader.vue`,
`ErrorPassthroughRulesModal.vue`, `SubscriptionsView.vue`) and 1 display component
(`PlatformTypeBadge.vue`). Each was written before `GATEWAY_PLATFORMS` constant existed
(2026-04-19) and never refactored to consume it.

This is **organizational drift**, not a logic bug — the canonical `GATEWAY_PLATFORMS`
exists; nothing consumes it for option-list generation.

## 3. Design

### 3.1 Single source of truth — `usePlatformOptions()`

Add `frontend/src/composables/usePlatformOptions.ts`:

```ts
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AccountPlatform } from '@/types'

const PLATFORM_LABELS: Record<AccountPlatform, string> = {
  anthropic:  'Anthropic',
  openai:     'OpenAI',
  gemini:     'Gemini',
  antigravity:'Antigravity',
  newapi:     'New API',
}

export interface PlatformOption {
  value: AccountPlatform
  label: string
}

/** Canonical platform options, ordered per GATEWAY_PLATFORMS. */
export function usePlatformOptions() {
  const options = computed<PlatformOption[]>(() =>
    GATEWAY_PLATFORMS.map(p => ({ value: p, label: PLATFORM_LABELS[p] })))

  /** Filter variant — prepend an "all" sentinel localized at call site. */
  const optionsWithAll = (allLabel: string) =>
    computed<Array<{ value: '' | AccountPlatform; label: string }>>(() => [
      { value: '', label: allLabel },
      ...options.value,
    ])

  return { options, optionsWithAll }
}
```

Rationale (Jobs simplicity + OPC automation):
- One canonical map, ordered by `GATEWAY_PLATFORMS` (which TypeScript already pins to
  `AccountPlatform` union — adding a 6th platform later requires touching one file).
- No i18n keys for platform labels: brand names (Anthropic / OpenAI / Gemini /
  Antigravity / New API) are not translated in this codebase today; introducing
  per-locale brand strings now would be premature.
- Filter variant is a function (not a computed) so callers pass their localized
  "all" label without globalizing it.

### 3.2 Account-creation tab — `CreateAccountModal.vue`

Append a 5th segmented-control button after the existing Antigravity button (around
line 139). Wire `form.platform = 'newapi'`. Add a `<div v-if="form.platform === 'newapi'">`
block immediately after the existing `antigravity` block (around line 707) that hosts
`<AccountNewApiPlatformFields v-model:channelType="..." v-model:baseUrl="..."
v-model:apiKey="..." :channel-type-options="..." :channel-types-loading="..."
:channel-types-error="..." :selected-channel-type-base-url="..." />`.

Data wiring:
- On modal open (or first time `form.platform === 'newapi'`), call
  `listChannelTypes()` from `@/api/admin/channels` and project to `{value, label}`
  pairs.
- `selectedChannelTypeBaseUrl` derived from the chosen `channel_type` row (used as
  the input placeholder so operators see the official upstream URL even when the
  field is blank).
- Submit → call existing `createAccount()` with `platform: 'newapi'`,
  `channel_type`, `base_url`, `api_key`, `name`. Backend validates
  `channel_type > 0` (admin_service.go:1565) so no client-side guard duplication is
  needed beyond a "required field" hint.

Following CLAUDE.md §5.x: `CreateAccountModal.vue` is upstream-derived; we only
**append** a tab and a `v-if` block (no rewrite).

### 3.3 Display correctness — `PlatformTypeBadge.vue`

Today line 74-79:

```ts
const platformLabel = computed(() => {
  if (props.platform === 'anthropic') return 'Anthropic'
  if (props.platform === 'openai') return 'OpenAI'
  if (props.platform === 'antigravity') return 'Antigravity'
  return 'Gemini'  // ← BUG: any unknown platform shown as Gemini
})
```

Same anti-pattern in `platformClass` and `typeClass` (default fallback = blue/Gemini
styling).

Fix: switch to explicit map keyed by `AccountPlatform` (which already includes
`newapi`); add `newapi` arm with cyan styling matching `gatewayPlatforms.ts:30`; add
a true unknown-platform branch (gray) for forward-compat.

```ts
const PLATFORM_LABEL: Record<AccountPlatform, string> = {
  anthropic:'Anthropic', openai:'OpenAI', gemini:'Gemini',
  antigravity:'Antigravity', newapi:'New API',
}
const PLATFORM_BG: Record<AccountPlatform, string> = {
  anthropic:'bg-orange-100 text-orange-700 dark:bg-orange-900/30 dark:text-orange-400',
  openai:   'bg-emerald-100 text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-400',
  gemini:   'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-400',
  antigravity:'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-400',
  newapi:   'bg-cyan-100 text-cyan-700 dark:bg-cyan-900/30 dark:text-cyan-400',
}
const PLATFORM_TYPE_BG: Record<AccountPlatform, string> = { /* same shape */ }
```

This eliminates the silent "Gemini fallback" bug for **any** platform unknown to the
component (newapi, future 6th, typo'd payload).

### 3.4 GroupsView option swap

Replace literals at `GroupsView.vue:2813-2818` and `:2820-2826` with the composable.
Filter variant uses `optionsWithAll(t('admin.groups.allPlatforms'))`.

### 3.5 Files Touched (prototype)

| Path | Change |
| --- | --- |
| `frontend/src/composables/usePlatformOptions.ts` | NEW — composable |
| `frontend/src/composables/__tests__/usePlatformOptions.spec.ts` | NEW — vitest regression test |
| `frontend/src/views/admin/GroupsView.vue` | replace 2 hardcoded option lists with composable (≤10 line diff) |
| `frontend/src/components/account/CreateAccountModal.vue` | + 5th segment button + `v-if newapi` block + `listChannelTypes` wiring |
| `frontend/src/components/common/PlatformTypeBadge.vue` | replace 4-arm `if/else` with `Record<AccountPlatform, …>` map; add newapi |
| `.testing/user-stories/stories/US-017-admin-ui-newapi-platform-pickers.md` | NEW — story |
| `.testing/user-stories/index.md` | + US-017 row |
| `docs/task-breakdown-admin-ui-newapi.md` | already written (stage-1 artifact) |
| `docs/approved/admin-ui-newapi-platform-end-to-end.md` | this doc |

No backend / Ent / Wire changes. No new dependencies.

## 4. Risk Analysis

| Risk | Likelihood | Impact | Mitigation |
| --- | --- | --- | --- |
| Existing 4-platform UX regresses after composable swap | Med | Medium | vitest asserts order = GATEWAY_PLATFORMS; manual click-through of 4 existing tabs in CreateAccountModal during prototype demo |
| Operators create newapi account with wrong channel_type / base_url and call fails | High (UX, not regression) | Low (clear backend error) | `AccountNewApiPlatformFields` already wires `fetchUpstreamModels` for self-test; required-asterisks visible |
| Translation gap — "New API" not localized | Low | Low | Brand names not localized today (4 existing platforms hardcoded in English) — defer to project i18n pass |
| `usePlatformOptions()` accidentally used in scopes that should hide newapi (e.g. SubscriptionsView) | Low | Med | Out-of-scope list explicitly excludes those views; reviewer checks call sites |
| Backend rejects `Platform: "newapi"` somewhere we didn't audit | Low | High | `admin_service.go:1565` is the only platform check in CreateAccount; `CreateGroup` accepts any string; covered by US-008..014 backend tests |

## 5. Acceptance (end-to-end demo for stage-2 approval)

The prototype is approval-worthy if a reviewer can, on a fresh dev stack:

1. Open `/admin/groups`, click 「创建分组」, see **5** platforms in dropdown including "New API". Create a `newapi` test group successfully.
2. Open `/admin/accounts`, click 「创建账号」, see **5** platform tabs. Click "New API". Channel type list loads. Pick e.g. DeepSeek, fill base_url + api_key, save. Account created, list refreshed.
3. The new account renders in the list with badge "New API" + cyan, **not** "Gemini" + blue.
4. Existing 4 platforms still work identically (no visual / behavioral diff).
5. `pnpm lint:check && pnpm typecheck && pnpm test:unit` green.
6. `./scripts/preflight.sh` green.

## 6. Stage-3 Follow-up (after this is approved & shipped)

Track in `docs/task-breakdown-admin-ui-newapi.md` §2 M1.3-M1.5, M2.4-M2.5, M3.2, M4.2-M4.4.
Each is a small, independent PR. Order suggestion: AccountTableFilters first (highest
operator visibility), then OpsDashboardHeader, then EditAccountModal, then bulk-edit
guardrails, then `platformColors.ts` fill-in, then ErrorPassthroughRulesModal (lowest
operational urgency).

## 7. Open Questions for Approval

1. **Display label**: "New API" vs "NewAPI" vs "newapi"? Prototype uses **"New API"** (matches channel-type catalog conventions).
2. **Color**: cyan (`gatewayPlatforms.ts` already declared). Approve or override?
3. **Localization**: keep brand names in English (current convention) or add i18n keys? Prototype: keep English.
4. **Default channel_type on create**: blank (force user to choose) or pre-select first item? Prototype: blank with required asterisk.
5. **AccountNewApiPlatformFields component path**: lives at `frontend/src/components/account/AccountNewApiPlatformFields.vue` — keep or move to a `*.tk.ts`-ish convention (CLAUDE.md §5)? Prototype: keep (the file already exists and follows TK companion-file convention by name; moving it would inflate diff).
