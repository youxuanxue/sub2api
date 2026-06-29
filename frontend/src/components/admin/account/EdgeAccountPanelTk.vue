<template>
  <!-- Indented under its parent stub row to read as a nested child: left margin =
       --select-col-width (past the checkbox) + a fixed step so the panel visibly sits
       inboard of the parent's name column, while the right edge fills the row width
       (block element, no w-fit / max-w cap) for a roomy full-width sub-table.
       --select-col-width is only defined inside .table-wrapper (table mode), so the
       fallback 0px collapses the indent to just the fixed step in the mobile card
       render (outside .table-wrapper). Inner overflow-x keeps a busy edge's sub-table
       scrolling internally rather than overflowing. -->
  <div class="dt-edge-panel ml-[calc(var(--select-col-width,0px)_+_1.5rem)] mr-2 my-1 overflow-hidden rounded-lg border border-primary-200 bg-primary-50/40 shadow-sm dark:border-dark-600 dark:bg-dark-800/60">
    <!-- Edge header -->
    <div class="flex flex-wrap items-center justify-between gap-2 border-b border-primary-100 px-4 py-1.5 dark:border-dark-700">
      <div class="flex min-w-0 items-center gap-2.5">
        <span :class="['inline-block h-2.5 w-2.5 flex-shrink-0 rounded-full', edge && edge.ok ? 'bg-green-500' : 'bg-red-500']"></span>
        <span class="text-sm font-semibold text-gray-900 dark:text-white">{{ edgeId }}</span>
        <span
          v-if="edge && !edge.stub_schedulable"
          class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
          :title="t('admin.edgeAccounts.stubPausedHint')"
        >{{ t('admin.edgeAccounts.stubPaused') }}</span>
        <span
          v-if="edge && stubRateLimited"
          class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
          :title="t('admin.edgeAccounts.stubRateLimitedHint')"
        >{{ t('admin.edgeAccounts.stubRateLimited') }}</span>
        <span
          v-if="edge && stubTempUnsched"
          class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
          :title="t('admin.edgeAccounts.stubTempUnschedHint')"
        >{{ t('admin.edgeAccounts.stubTempUnsched') }}</span>
        <span v-if="edge" class="truncate text-xs text-gray-400 dark:text-gray-500">{{ edge.base_url }}</span>
        <span
          v-if="panelScope"
          class="flex-shrink-0 text-xs font-medium text-primary-600/80 dark:text-primary-300/80"
          :title="t('admin.accounts.edgePanel.scopeHint')"
        >{{ panelScope.kind === 'group'
            ? t('admin.accounts.edgePanel.scopeGroup', { group: panelScope.name, count: panelScope.count })
            : t('admin.accounts.edgePanel.scopePool', { platform: panelScope.name, count: panelScope.count }) }}</span>
      </div>
      <div class="flex items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
        <span v-if="edge && edge.ok">
          {{ t('admin.edgeAccounts.accountCount', { count: edge.accounts.length }) }}
          · {{ t('admin.edgeAccounts.schedulableCount', { count: schedulableCount(edge) }) }}
        </span>
        <button
          v-if="edge && edge.ok"
          type="button"
          class="btn btn-secondary btn-sm inline-flex items-center gap-1"
          :disabled="managing"
          @click="openEdgeManage"
        >
          <Icon name="link" size="sm" :class="managing ? 'animate-pulse' : ''" />
          {{ t('admin.accounts.edgePanel.manageWholeEdge') }}
        </button>
      </div>
    </div>

    <!-- Loading (edge data not yet available) -->
    <div v-if="!edge && loading" class="px-4 py-4">
      <div class="space-y-2">
        <div v-for="i in 2" :key="i" class="h-5 w-full animate-pulse rounded bg-gray-200 dark:bg-dark-700"></div>
      </div>
    </div>
    <!-- Edge not discovered (prod stub errored / disabled / undiscoverable): still
         actionable — offer the jump to the edge so the operator can manage/configure
         it there. The handoff mints by edge_id (any active stub for the edge), so it
         works even when THIS stub is errored, as long as the edge has a reachable
         stub. Without this the card was a dead end (#913 follow-up). -->
    <div v-else-if="!edge" class="flex flex-wrap items-center justify-center gap-3 px-4 py-4 text-center text-sm text-gray-400 dark:text-gray-500">
      <span>{{ error || t('admin.accounts.edgePanel.notDiscovered') }}</span>
      <button
        v-if="edgeId"
        type="button"
        class="btn btn-secondary btn-sm inline-flex items-center gap-1"
        :disabled="managing"
        @click="openEdgeManage"
      >
        <Icon name="link" size="sm" :class="managing ? 'animate-pulse' : ''" />
        {{ t('admin.accounts.edgePanel.manageWholeEdge') }}
      </button>
    </div>
    <!-- Unreachable edge: inline error + retry (not a broken empty expand) -->
    <div v-else-if="!edge.ok" class="flex flex-wrap items-center justify-center gap-3 px-4 py-4 text-center text-sm">
      <span class="text-red-600 dark:text-red-400">{{ edge.error || t('admin.edgeAccounts.loadFailed') }}</span>
      <button type="button" class="btn btn-secondary btn-sm inline-flex items-center gap-1" :disabled="loading" @click="emit('retry')">
        <Icon name="sync" size="sm" :class="loading ? 'animate-spin' : ''" />
        {{ t('admin.accounts.edgePanel.retry') }}
      </button>
    </div>
    <!-- Accounts (abnormal-first) -->
    <div v-else-if="edge.accounts.length" class="overflow-x-auto">
      <table class="min-w-full divide-y divide-primary-100 text-sm dark:divide-dark-700">
        <thead class="bg-primary-50/60 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900 dark:text-gray-400">
          <tr>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.name') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.platformType') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.capacity') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.status') }}</th>
            <th class="px-4 py-1.5 text-center font-medium">{{ t('admin.accounts.columns.schedulable') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.todayStats') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.groups') }}</th>
            <th class="px-4 py-1.5 text-left font-medium">{{ t('admin.accounts.columns.usageWindows') }}</th>
            <th class="px-4 py-1.5 text-right font-medium">{{ t('admin.accounts.columns.priority') }}</th>
            <th class="px-4 py-1.5 text-right font-medium">{{ t('admin.accounts.edgePanel.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-primary-50 dark:divide-dark-700/50">
          <tr v-for="acct in sortedAccounts" :key="acct.id" class="hover:bg-primary-50/50 dark:hover:bg-dark-700/40">
            <td class="px-4 py-1.5 align-top">
              <div class="font-medium text-gray-900 dark:text-white">{{ acct.name }}</div>
              <div class="font-mono text-xs text-gray-400 dark:text-gray-500" :title="t('admin.edgeAccounts.accountIdHint')">ID: {{ acct.id }}</div>
              <div v-if="shouldShowEdgeAccountError(acct)" class="mt-0.5 max-w-xs truncate text-xs text-red-500" :title="acct.error_message">{{ acct.error_message }}</div>
              <div
                v-if="acct.temp_unschedulable_reason"
                class="mt-0.5 max-w-xs truncate text-xs"
                :class="isTempUnschedActive(acct) ? 'text-amber-600 dark:text-amber-400' : 'text-gray-400 dark:text-gray-500'"
                :title="acct.temp_unschedulable_reason"
              >
                <span v-if="!isTempUnschedActive(acct)" class="mr-1 rounded bg-gray-100 px-1 text-[10px] font-medium dark:bg-dark-700">{{ t('admin.edgeAccounts.cooldownRecovered') }}</span>
                {{ acct.temp_unschedulable_reason }}
              </div>
              <div v-if="acct.notes" class="mt-0.5 block max-w-xs whitespace-pre-wrap break-words text-xs text-gray-500 dark:text-gray-400" :title="acct.notes">{{ acct.notes }}</div>
            </td>
            <td class="px-4 py-1.5 align-top text-gray-600 dark:text-gray-300">
              <PlatformTypeBadge
                :platform="(acct.platform as AccountPlatform)"
                :type="(acct.type as AccountType)"
                :plan-type="(accountVm(acct).accountLike.credentials?.plan_type as string | undefined)"
                :subscription-expires-at="(accountVm(acct).accountLike.credentials?.subscription_expires_at as string | undefined)"
              />
              <div v-if="acct.channel_type" class="mt-0.5 text-xs text-gray-400 dark:text-gray-500">ch{{ acct.channel_type }}</div>
              <!-- Operator-set account 到期 (same field as prod list expires_at column). -->
              <div
                v-if="accountVm(acct).accountLike.expires_at"
                class="mt-0.5 text-[10px] leading-tight text-gray-400 dark:text-gray-500"
              >
                {{ t('admin.accounts.columns.expiresAt') }}
                {{ formatDateOnly(new Date(accountVm(acct).accountLike.expires_at! * 1000)) }}
              </div>
            </td>
            <td class="px-4 py-1.5 align-top">
              <AccountCapacityCell :account="accountVm(acct).accountLike" />
            </td>
            <td class="px-4 py-1.5 align-top">
              <AccountStatusIndicator :account="accountVm(acct).accountLike" />
            </td>
            <td class="px-4 py-1.5 text-center align-top">
              <button
                type="button"
                :disabled="busyId === acct.id"
                class="relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 dark:focus:ring-offset-dark-800"
                :class="[acct.schedulable ? 'bg-primary-500 hover:bg-primary-600' : 'bg-gray-200 hover:bg-gray-300 dark:bg-dark-600 dark:hover:bg-dark-500']"
                :title="acct.schedulable ? t('admin.accounts.schedulableEnabled') : t('admin.accounts.schedulableDisabled')"
                @click="toggleSchedulable(acct)"
              >
                <span class="pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out" :class="[acct.schedulable ? 'translate-x-4' : 'translate-x-0']" />
              </button>
            </td>
            <td class="px-4 py-1.5 align-top">
              <AccountTodayStatsCell :stats="accountVm(acct).windowStats" />
            </td>
            <td class="px-4 py-1.5 align-top text-gray-600 dark:text-gray-300">
              <span v-if="acct.groups && acct.groups.length">{{ acct.groups.join(', ') }}</span>
              <span v-else class="text-gray-300 dark:text-gray-600">—</span>
            </td>
            <td class="px-4 py-1.5 align-top">
              <AccountUsageCell
                :account="accountVm(acct).accountLike"
                :usage-override="usageOverrideFor(acct)"
              />
            </td>
            <td class="px-4 py-1.5 align-top text-right text-gray-700 dark:text-gray-200">{{ acct.priority }}</td>
            <td class="px-4 py-1.5 text-right align-top">
              <button
                type="button"
                class="inline-flex h-7 w-7 items-center justify-center rounded text-gray-400 hover:bg-gray-100 hover:text-gray-600 dark:hover:bg-dark-700 dark:hover:text-gray-300"
                :disabled="busyId === acct.id"
                :title="t('admin.accounts.edgePanel.actions')"
                @click="openMenu(acct, $event)"
              >
                <Icon v-if="busyId === acct.id" name="sync" size="sm" class="animate-spin" />
                <span v-else class="text-lg leading-none">⋯</span>
              </button>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <!-- Reachable but empty (this stub key's group has no accounts on the edge):
         actionable, not a blank — jump to the edge to configure it. -->
    <div v-else class="flex flex-wrap items-center justify-center gap-3 px-4 py-4 text-center text-sm text-gray-400 dark:text-gray-500">
      <span>{{ t('admin.accounts.edgePanel.groupEmpty') }}</span>
      <button
        type="button"
        class="btn btn-secondary btn-sm inline-flex items-center gap-1"
        :disabled="managing"
        @click="openEdgeManage"
      >
        <Icon name="link" size="sm" :class="managing ? 'animate-pulse' : ''" />
        {{ t('admin.accounts.edgePanel.manageWholeEdge') }}
      </button>
    </div>

    <EdgeAccountActionMenuTk
      :show="!!menuAccount"
      :account="menuAccount"
      :position="menuPosition"
      @close="closeMenu"
      @query-usage="onQueryUsage"
      @clear-rate-limit="onClearRateLimit"
      @clear-temp-unschedulable="onClearTempUnsched"
      @reset-quota="onResetQuota"
      @manage="openEdgeManage"
    />
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Icon } from '@/components/icons'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountTodayStatsCell from '@/components/account/AccountTodayStatsCell.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import EdgeAccountActionMenuTk from '@/components/admin/account/EdgeAccountActionMenuTk.vue'
import PlatformTypeBadge from '@/components/common/PlatformTypeBadge.vue'
import { formatDateOnly } from '@/utils/format'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { Account, AccountUsageInfo, AccountPlatform, AccountType } from '@/types'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'
import {
  schedulableCount,
  accountVm,
  isTempUnschedActive,
  shouldShowEdgeAccountError,
  isStubRateLimited,
  isStubTempUnschedActive
} from '@/utils/edgeAccounts.tk'
import { sortEdgeAccountsAbnormalFirst } from '@/utils/accountsEdgePanels.tk'

const props = defineProps<{
  /** The prod cc-<edge> mirror-stub row this panel expands under. */
  stub: Account
  /** The resolved edge slice (null until discovered / while edge data loads). */
  edge: EdgeAccountsResult | null
  /** Edge data load state (from the panels composable). */
  loading?: boolean
  error?: string | null
  /**
   * Bumped by AccountsView.handleManualRefresh (the explicit 刷新 button ONLY — not
   * the auto-refresh tick). On change this panel pulls live kiro credits once for
   * each of its kiro accounts. kiro is the only platform without an organic passive
   * refresh (anthropic/openai windows ride gateway response headers), so this is the
   * one「刷新即拉一次」trigger that hits CodeWhisperer — bounded to operator clicks.
   */
  refreshKiroToken?: number
}>()

const emit = defineEmits<{
  // A whitelisted op mutated an edge account → carry the updated DTO up so the
  // composable can surgically patch the edge data, and pin this panel open.
  mutated: [account: EdgeAccountSummary]
  // Operator asked to re-fetch after an unreachable edge → parent refreshes the
  // by-stub aggregate (no per-panel fetch; the composable owns the data lifecycle).
  retry: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const edgeId = computed(() => props.stub.edge_id ?? '')
const stubRateLimited = computed(() => (props.edge ? isStubRateLimited(props.edge) : false))
const stubTempUnsched = computed(() => (props.edge ? isStubTempUnschedActive(props.edge) : false))

// 异常置顶: abnormal edge accounts float to the top of this panel so the operator
// reaches what needs attention first, regardless of how long the list is.
const sortedAccounts = computed(() =>
  props.edge ? sortEdgeAccountsAbnormalFirst(props.edge.accounts) : []
)

// Precise-correspondence footnote (§5.7 读得懂): name the edge-side group this stub
// key actually schedules ("调度自 <group> 组 · 共 N 个"), or the whole-platform pool
// for a universal / single-pool key. null when the edge is unreachable / empty (the
// dedicated states cover those).
const panelScope = computed(() => {
  const e = props.edge
  if (!e || !e.ok) return null
  const count = e.accounts.length
  if (e.edge_group) return { kind: 'group' as const, name: e.edge_group, count }
  if (e.stub_platform) return { kind: 'pool' as const, name: e.stub_platform, count }
  return null
})

// Per-account busy flag (one op at a time per row).
const busyId = ref<number | null>(null)
// Freshly active-queried usage, overriding the passive windows for that row until
// the panel re-mounts. Keyed by edge-local account id.
const activeUsage = ref<Map<number, AccountUsageInfo>>(new Map())
function usageOverrideFor(acct: EdgeAccountSummary): AccountUsageInfo | null {
  return activeUsage.value.get(acct.id) ?? accountVm(acct).usageInfo
}

// --- per-account action menu (Teleported; positioned at the click) ---
const menuAccount = ref<EdgeAccountSummary | null>(null)
const menuPosition = ref<{ top: number; left: number } | null>(null)
function openMenu(acct: EdgeAccountSummary, event: MouseEvent) {
  const btn = (event.currentTarget as HTMLElement).getBoundingClientRect()
  // Right-align a 224px (w-56) menu under the trigger, clamped to the viewport.
  const left = Math.max(8, Math.min(btn.right - 224, window.innerWidth - 232))
  menuAccount.value = acct
  menuPosition.value = { top: btn.bottom + 4, left }
}
function closeMenu() {
  menuAccount.value = null
  menuPosition.value = null
}

// --- write ops (whitelisted, status-class; credentials never touched here) ---
async function runOp(acct: EdgeAccountSummary, op: () => Promise<EdgeAccountSummary>) {
  if (busyId.value !== null || !edgeId.value) return
  busyId.value = acct.id
  try {
    const updated = await op()
    emit('mutated', updated)
    appStore.showSuccess(t('admin.accounts.edgePanel.opSuccess'))
  } catch {
    appStore.showError(t('admin.accounts.edgePanel.opFailed'))
  } finally {
    busyId.value = null
  }
}
function onClearRateLimit() {
  const a = menuAccount.value
  if (a) void runOp(a, () => adminAPI.edgeAccounts.clearRateLimit(edgeId.value, a.id))
}
function onResetQuota() {
  const a = menuAccount.value
  if (a) void runOp(a, () => adminAPI.edgeAccounts.resetQuota(edgeId.value, a.id))
}
function onClearTempUnsched() {
  const a = menuAccount.value
  if (a) void runOp(a, () => adminAPI.edgeAccounts.clearTempUnschedulable(edgeId.value, a.id))
}
function toggleSchedulable(acct: EdgeAccountSummary) {
  void runOp(acct, () => adminAPI.edgeAccounts.setSchedulable(edgeId.value, acct.id, !acct.schedulable))
}

// Active-query one edge account's usage into the override map (force=true hits the
// edge's GetUsage → upstream). Shared by the per-row menu「查询」and the manual-
// refresh kiro pull; the latter is silent (best-effort, no global error toast).
async function fetchActiveUsageInto(accountId: number, silent: boolean): Promise<void> {
  if (!edgeId.value) return
  try {
    const usage = await adminAPI.edgeAccounts.getUsage(edgeId.value, accountId, 'active', true)
    const next = new Map(activeUsage.value)
    next.set(accountId, usage)
    activeUsage.value = next
  } catch {
    if (!silent) appStore.showError(t('admin.accounts.edgePanel.queryFailed'))
  }
}

async function onQueryUsage() {
  const a = menuAccount.value
  if (!a || !edgeId.value) return
  busyId.value = a.id
  try {
    await fetchActiveUsageInto(a.id, false)
  } finally {
    busyId.value = null
  }
}

// Explicit manual refresh (parent bumps refreshKiroToken): pull live credits once
// for every kiro account in THIS panel. Only kiro — anthropic/openai windows stay
// on the passive DTO (they refresh organically from gateway headers). The auto-
// refresh tick never bumps this token, so kiro upstream calls stay bounded to the
// operator's 刷新 clicks (the「仅按需」guarantee).
watch(
  () => props.refreshKiroToken,
  (next, prev) => {
    if (!next || next === prev) return
    for (const a of props.edge?.accounts ?? []) {
      if (a.platform === 'kiro') void fetchActiveUsageInto(a.id, true)
    }
  }
)

// --- "manage on edge" handoff (edge-level; credential-class management lives here) ---
const managing = ref(false)
async function openEdgeManage() {
  if (managing.value || !edgeId.value) return
  managing.value = true
  // Open the tab synchronously inside the click so the post-await navigation is not
  // treated as a popup (mirrors EdgeAccountsView.openEdgeManage).
  const tab = window.open('', '_blank')
  try {
    const res = await adminAPI.edgeAccounts.adminSession(edgeId.value)
    if (tab) tab.location.href = res.handoff_url
    else window.location.href = res.handoff_url
  } catch {
    if (tab) tab.close()
    appStore.showError(t('admin.edgeAccounts.manageFailed'))
  } finally {
    managing.value = false
  }
}
</script>
