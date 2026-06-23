<template>
    <div class="space-y-6">
      <!-- Sticky header + summary: pinned just below the global AppHeader (h-16 =
           top-16) so the operator can hit 刷新 and read the fleet totals at any
           scroll depth in the per-edge list. Negative margins bleed the opaque
           backdrop to the edges of the main padding so scrolled cards never peek
           through; z-20 stays under AppHeader's z-30. -->
      <div
        class="sticky top-16 z-20 -mx-4 space-y-4 bg-gray-50/95 px-4 pb-4 pt-1 backdrop-blur md:-mx-6 md:px-6 lg:-mx-8 lg:px-8 dark:bg-dark-950/95"
      >
        <!-- Header -->
        <div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
          <div>
            <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.edgeAccounts.title') }}</h1>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.edgeAccounts.description') }}</p>
          </div>
          <!-- Compact single-row filter bar: narrowed selects (uniform w-32 instead of
               w-36, px-3 instead of the .input default px-4) + tighter gaps keep 平台/
               状态/分组/最近拉取/自动刷新/刷新 on one line instead of wrapping 刷新 to a second
               row. w-32 still fits the longest option of each fixed-vocabulary select
               (platform 'antigravity', status '临时不可调度', group '未分配分组'). -->
          <div class="flex flex-wrap items-center gap-x-2 gap-y-2">
            <label class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400">
              <span>{{ t('admin.edgeAccounts.platformFilter') }}</span>
              <select
                class="input w-32 px-3"
                :value="platform"
                @change="setPlatform(($event.target as HTMLSelectElement).value)"
              >
                <option value="all">{{ t('admin.edgeAccounts.allPlatforms') }}</option>
                <option v-for="p in PLATFORM_OPTIONS" :key="p" :value="p">{{ p }}</option>
              </select>
            </label>
            <!-- Status + group filters narrow the already-fetched aggregate on the
                 prod side (client-only, no per-edge re-query). 分组 keys on the PROD
                 stub's group; 状态 combines the prod stub's status with each edge
                 account's status (see useTkEdgeAccounts / edgeAccounts.tk.ts). The
                 hint titles spell the semantics out for the operator. -->
            <label
              class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400"
              :title="t('admin.edgeAccounts.statusFilterHint')"
            >
              <span>{{ t('admin.edgeAccounts.statusFilter') }}</span>
              <select
                class="input w-32 px-3"
                :value="statusFilter"
                @change="setStatusFilter(($event.target as HTMLSelectElement).value)"
              >
                <option value="">{{ t('admin.edgeAccounts.allStatus') }}</option>
                <option value="active">{{ t('admin.accounts.status.active') }}</option>
                <option value="inactive">{{ t('admin.accounts.status.inactive') }}</option>
                <option value="error">{{ t('admin.accounts.status.error') }}</option>
                <option value="rate_limited">{{ t('admin.accounts.status.rateLimited') }}</option>
                <option value="temp_unschedulable">{{ t('admin.accounts.status.tempUnschedulable') }}</option>
                <option value="unschedulable">{{ t('admin.accounts.status.unschedulable') }}</option>
              </select>
            </label>
            <label
              class="flex items-center gap-1.5 text-xs text-gray-500 dark:text-gray-400"
              :title="t('admin.edgeAccounts.groupFilterHint')"
            >
              <span>{{ t('admin.edgeAccounts.groupFilter') }}</span>
              <select
                class="input w-32 px-3"
                :value="groupFilter"
                @change="setGroupFilter(($event.target as HTMLSelectElement).value)"
              >
                <option value="">{{ t('admin.edgeAccounts.allGroups') }}</option>
                <option value="ungrouped">{{ t('admin.edgeAccounts.ungroupedGroup') }}</option>
                <option v-for="g in groupOptions" :key="g" :value="g">{{ g }}</option>
              </select>
            </label>
            <span v-if="lastFetchedAt" class="text-xs text-gray-400 dark:text-gray-500">
              {{ t('admin.edgeAccounts.lastFetched') }}: {{ formatDateTime(lastFetchedAt) }}
            </span>
            <!-- Auto-refresh control: same toggle + interval dropdown + countdown the
                 admin accounts page uses (reuses its i18n keys). The composable owns
                 the ETag/304 + incremental edge merge behind it. -->
            <div class="relative" ref="autoRefreshDropdownRef">
              <button
                type="button"
                class="btn btn-secondary inline-flex items-center gap-2"
                :title="t('admin.accounts.autoRefresh')"
                @click="showAutoRefreshDropdown = !showAutoRefreshDropdown"
              >
                <Icon name="refresh" size="sm" :class="autoRefreshEnabled ? 'animate-spin' : ''" />
                <span>
                  {{
                    autoRefreshEnabled
                      ? t('admin.accounts.autoRefreshCountdown', { seconds: autoRefreshCountdown })
                      : t('admin.accounts.autoRefresh')
                  }}
                </span>
              </button>
              <div
                v-if="showAutoRefreshDropdown"
                class="absolute right-0 z-50 mt-2 w-56 origin-top-right rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800"
              >
                <div class="p-2">
                  <button
                    type="button"
                    class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                    @click="setAutoRefreshEnabled(!autoRefreshEnabled)"
                  >
                    <span>{{ t('admin.accounts.enableAutoRefresh') }}</span>
                    <Icon v-if="autoRefreshEnabled" name="check" size="sm" class="text-primary-500" />
                  </button>
                  <div class="my-1 border-t border-gray-100 dark:border-gray-700"></div>
                  <button
                    v-for="sec in autoRefreshIntervals"
                    :key="sec"
                    type="button"
                    class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                    @click="setAutoRefreshInterval(sec)"
                  >
                    <span>{{ autoRefreshIntervalLabel(sec) }}</span>
                    <Icon v-if="autoRefreshIntervalSeconds === sec" name="check" size="sm" class="text-primary-500" />
                  </button>
                </div>
              </div>
            </div>
            <button
              type="button"
              class="btn btn-secondary inline-flex items-center gap-2"
              :disabled="loading"
              @click="() => fetch({ force: true })"
            >
              <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
              {{ t('admin.edgeAccounts.refresh') }}
            </button>
          </div>
        </div>

        <!-- Summary bar -->
        <div v-if="!loading || edges.length" class="flex flex-wrap items-center gap-3 text-sm">
          <span class="rounded-md bg-gray-100 px-3 py-1 text-gray-700 dark:bg-dark-700 dark:text-gray-200">
            {{ t('admin.edgeAccounts.summaryEdges', { ok: okEdges.length, total: edges.length }) }}
          </span>
          <span class="rounded-md bg-gray-100 px-3 py-1 text-gray-700 dark:bg-dark-700 dark:text-gray-200">
            {{ t('admin.edgeAccounts.summaryAccounts', { count: totalAccounts }) }}
          </span>
          <span
            v-if="failedEdges.length"
            class="rounded-md bg-red-100 px-3 py-1 text-red-700 dark:bg-red-900/40 dark:text-red-300"
          >
            {{ t('admin.edgeAccounts.summaryFailed', { count: failedEdges.length }) }}
          </span>

          <!-- Aggregated current/capacity across all currently-schedulable accounts:
               Σ(live gauge) / Σ(configured cap) for concurrency / base RPM / sessions,
               mirroring AccountCapacityCell's per-account "current/capacity" shape. -->
          <template v-if="totals.count">
            <span class="self-center text-xs text-gray-400 dark:text-gray-500">
              {{ t('admin.edgeAccounts.summaryConfigLabel', { count: totals.count }) }}
            </span>
            <span class="rounded-md bg-primary-50 px-3 py-1 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
              {{ t('admin.edgeAccounts.summaryConcurrency', { current: totals.concurrency.current, value: totals.concurrency.max }) }}
            </span>
            <span class="rounded-md bg-primary-50 px-3 py-1 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
              {{ t('admin.edgeAccounts.summaryBaseRpm', { current: totals.rpm.current, base: totals.rpm.base, sticky: totals.rpm.sticky }) }}
            </span>
            <span class="rounded-md bg-primary-50 px-3 py-1 text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
              {{ t('admin.edgeAccounts.summarySessions', { current: totals.sessions.current, value: totals.sessions.max }) }}
            </span>
          </template>
        </div>
      </div>

      <!-- Loading -->
      <div v-if="loading && !edges.length" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <!-- Error (discovery / request failed) -->
      <div
        v-else-if="error"
        class="rounded-lg border border-red-200 bg-red-50 px-4 py-3 text-sm text-red-700 dark:border-red-900/50 dark:bg-red-900/20 dark:text-red-300"
      >
        {{ error }}
      </div>

      <!-- Empty: no edges discovered at all -->
      <div
        v-else-if="!edges.length"
        class="rounded-lg border border-gray-100 bg-white px-4 py-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400"
      >
        {{ t('admin.edgeAccounts.noEdges') }}
      </div>

      <!-- Edges exist but the active status/group filter matched nothing -->
      <div
        v-else-if="!displayEdges.length"
        class="rounded-lg border border-gray-100 bg-white px-4 py-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400"
      >
        {{ t('admin.edgeAccounts.noMatch') }}
      </div>

      <!-- Per-edge sections -->
      <div v-else class="space-y-5">
        <section
          v-for="edge in displayEdges"
          :key="edge.edge_id"
          class="overflow-hidden rounded-lg border border-gray-100 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800"
        >
          <!-- Edge header -->
          <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
            <div class="flex min-w-0 items-center gap-3">
              <span :class="['inline-block h-2.5 w-2.5 flex-shrink-0 rounded-full', edge.ok ? 'bg-green-500' : 'bg-red-500']"></span>
              <span class="font-semibold text-gray-900 dark:text-white">{{ edge.edge_id }}</span>
              <!-- The prod-side mirror stub for this edge was 关调度: the edge stays
                   reachable but prod no longer routes traffic to it. Orthogonal to
                   the ok/unreachable dot, so flag it explicitly here. -->
              <span
                v-if="!edge.stub_schedulable"
                class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                :title="t('admin.edgeAccounts.stubPausedHint')"
              >
                {{ t('admin.edgeAccounts.stubPaused') }}
              </span>
              <!-- The prod stub's own cooldown (rate-limit / temp-unschedulable) is
                   live: prod's relay to this edge is throttled even though the edge
                   itself may be reachable. Surfaced so a stub-driven 状态 filter match —
                   which keeps the edge's otherwise-healthy rows via the stub-OR-account
                   rule — has a visible cause, the same way the 调度已关闭 badge does. -->
              <span
                v-if="isStubRateLimited(edge)"
                class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                :title="t('admin.edgeAccounts.stubRateLimitedHint')"
              >
                {{ t('admin.edgeAccounts.stubRateLimited') }}
              </span>
              <span
                v-if="isStubTempUnschedActive(edge)"
                class="inline-flex flex-shrink-0 items-center rounded-md bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700 dark:bg-amber-900/40 dark:text-amber-300"
                :title="t('admin.edgeAccounts.stubTempUnschedHint')"
              >
                {{ t('admin.edgeAccounts.stubTempUnsched') }}
              </span>
              <span class="truncate text-xs text-gray-400 dark:text-gray-500">{{ edge.base_url }}</span>
            </div>
            <div class="flex items-center gap-3 text-xs text-gray-500 dark:text-gray-400">
              <span v-if="edge.ok">
                {{ t('admin.edgeAccounts.accountCount', { count: edge.accounts.length }) }}
                · {{ t('admin.edgeAccounts.schedulableCount', { count: schedulableCount(edge) }) }}
              </span>
              <span v-else class="text-red-600 dark:text-red-400">{{ edge.error }}</span>
              <!-- Jump into this edge's own /admin/accounts, auto-logged-in, to
                   create/edit accounts natively (incl. OAuth) on the edge itself. -->
              <button
                v-if="edge.ok"
                type="button"
                class="btn btn-secondary btn-sm inline-flex items-center gap-1"
                :disabled="managingEdge === edge.edge_id"
                @click="openEdgeManage(edge.edge_id)"
              >
                <Icon name="link" size="sm" :class="managingEdge === edge.edge_id ? 'animate-pulse' : ''" />
                {{ t('admin.edgeAccounts.manageAccounts') }}
              </button>
            </div>
          </div>

          <!-- Accounts table -->
          <div v-if="edge.ok && edge.accounts.length" class="overflow-x-auto">
            <table class="min-w-full divide-y divide-gray-100 text-sm dark:divide-dark-700">
              <thead class="bg-gray-50 text-xs uppercase tracking-wide text-gray-500 dark:bg-dark-900 dark:text-gray-400">
                <tr>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.name') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.platformType') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.capacity') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.usageWindows') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.state') }}</th>
                  <th class="px-4 py-2 text-right font-medium">{{ t('admin.edgeAccounts.columns.priority') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.groups') }}</th>
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.lastUsed') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-50 dark:divide-dark-700/50">
                <tr v-for="acct in edge.accounts" :key="acct.id" class="hover:bg-gray-50 dark:hover:bg-dark-700/40">
                  <td class="px-4 py-2 align-top">
                    <div class="font-medium text-gray-900 dark:text-white">{{ acct.name }}</div>
                    <div class="font-mono text-xs text-gray-400 dark:text-gray-500" :title="t('admin.edgeAccounts.accountIdHint')">ID: {{ acct.id }}</div>
                    <div v-if="shouldShowEdgeAccountError(acct)" class="mt-0.5 max-w-xs truncate text-xs text-red-500" :title="acct.error_message">
                      {{ acct.error_message }}
                    </div>
                    <!-- temp-unschedulable reason shown inline: the reused AccountStatusIndicator's
                         temp-unsched badge opens an admin modal we don't have here (read-only), so
                         surface the reason passively rather than behind an inert click.
                         The reason persists in the DB after the cooldown lapses (forensic
                         breadcrumb, never cleared), so gate the alarming amber styling on
                         isTempUnschedActive — an expired cooldown renders dimmed with a 已恢复
                         tag instead of reading as a live problem. -->
                    <div
                      v-if="acct.temp_unschedulable_reason"
                      class="mt-0.5 max-w-xs truncate text-xs"
                      :class="isTempUnschedActive(acct) ? 'text-amber-600 dark:text-amber-400' : 'text-gray-400 dark:text-gray-500'"
                      :title="acct.temp_unschedulable_reason"
                    >
                      <span
                        v-if="!isTempUnschedActive(acct)"
                        class="mr-1 rounded bg-gray-100 px-1 text-[10px] font-medium dark:bg-dark-700"
                      >{{ t('admin.edgeAccounts.cooldownRecovered') }}</span>
                      {{ acct.temp_unschedulable_reason }}
                    </div>
                    <!-- Operator 备注, mirroring the admin accounts page name cell.
                         whitespace-pre-wrap break-words (not truncate) so multi-line
                         notes render verbatim, matching the #618 AccountsView fix. -->
                    <div v-if="acct.notes" class="mt-0.5 block max-w-xs whitespace-pre-wrap break-words text-xs text-gray-500 dark:text-gray-400" :title="acct.notes">
                      {{ acct.notes }}
                    </div>
                  </td>
                  <td class="px-4 py-2 align-top text-gray-600 dark:text-gray-300">
                    <span>{{ acct.platform }}</span>
                    <span class="text-gray-400 dark:text-gray-500"> / {{ acct.type }}</span>
                    <span v-if="acct.channel_type" class="text-gray-400 dark:text-gray-500"> · ch{{ acct.channel_type }}</span>
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountCapacityCell :account="accountVm(acct).accountLike" :today-stats="accountVm(acct).windowStats" />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountUsageCell
                      :account="accountVm(acct).accountLike"
                      :today-stats="accountVm(acct).windowStats"
                      :usage-override="accountVm(acct).usageInfo"
                    />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountStatusIndicator :account="accountVm(acct).accountLike" />
                  </td>
                  <td class="px-4 py-2 align-top text-right text-gray-700 dark:text-gray-200">{{ acct.priority }}</td>
                  <td class="px-4 py-2 align-top text-gray-600 dark:text-gray-300">
                    <span v-if="acct.groups && acct.groups.length">{{ acct.groups.join(', ') }}</span>
                    <span v-else class="text-gray-300 dark:text-gray-600">—</span>
                  </td>
                  <td class="px-4 py-2 align-top text-xs text-gray-500 dark:text-gray-400">
                    {{ acct.last_used_at ? formatRelativeTime(acct.last_used_at) : '—' }}
                  </td>
                </tr>
              </tbody>
            </table>
          </div>

          <!-- Reachable but empty -->
          <div
            v-else-if="edge.ok"
            class="px-4 py-6 text-center text-sm text-gray-400 dark:text-gray-500"
          >
            {{ t('admin.edgeAccounts.edgeEmpty') }}
          </div>
        </section>
      </div>
    </div>
  </template>

<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import {
  schedulableCount,
  accountVm,
  isTempUnschedActive,
  shouldShowEdgeAccountError,
  isStubRateLimited,
  isStubTempUnschedActive
} from '@/utils/edgeAccounts.tk'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'

const { t } = useI18n()
const appStore = useAppStore()

// Which edge is currently minting a handoff (disables its button). Opening the
// edge's own /admin/accounts in a new tab keeps this read-only overview open for
// managing several edges in sequence.
const managingEdge = ref<string | null>(null)

async function openEdgeManage(edgeId: string) {
  if (managingEdge.value) return
  managingEdge.value = edgeId
  // Open the tab synchronously inside the click so the browser doesn't treat the
  // post-await window.open as a popup; navigate it once the URL is minted.
  const tab = window.open('', '_blank')
  try {
    const res = await adminAPI.edgeAccounts.adminSession(edgeId)
    if (tab) {
      tab.location.href = res.handoff_url
    } else {
      // Popup blocked — fall back to same-tab navigation.
      window.location.href = res.handoff_url
    }
  } catch {
    if (tab) tab.close()
    appStore.showError(t('admin.edgeAccounts.manageFailed'))
  } finally {
    managingEdge.value = null
  }
}

// Concrete platforms the filter offers besides "all". Sourced from the canonical
// GATEWAY_PLATFORMS list (single source of truth, mirrors the backend allowlist
// in edge_tk_accounts_handler.go) so a new platform never silently goes stale here.
const PLATFORM_OPTIONS = GATEWAY_PLATFORMS

const {
  platform,
  edges,
  displayEdges,
  loading,
  error,
  lastFetchedAt,
  okEdges,
  failedEdges,
  totalAccounts,
  totals,
  fetch,
  setPlatform,
  statusFilter,
  groupFilter,
  groupOptions,
  setStatusFilter,
  setGroupFilter,
  autoRefreshEnabled,
  autoRefreshIntervalSeconds,
  autoRefreshIntervals,
  autoRefreshCountdown,
  setAutoRefreshEnabled,
  setAutoRefreshInterval
} = useTkEdgeAccounts()
// Initial fetch + periodic auto-refresh (ETag/304 + incremental edge merge) are
// owned by useTkEdgeAccounts; only the dropdown open/close lives in the view.

const showAutoRefreshDropdown = ref(false)
const autoRefreshDropdownRef = ref<HTMLElement | null>(null)

// Interval option labels, reusing the admin accounts page's i18n keys.
const autoRefreshIntervalLabel = (sec: number) => {
  if (sec === 5) return t('admin.accounts.refreshInterval5s')
  if (sec === 10) return t('admin.accounts.refreshInterval10s')
  if (sec === 15) return t('admin.accounts.refreshInterval15s')
  if (sec === 30) return t('admin.accounts.refreshInterval30s')
  return `${sec}s`
}

const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as Node
  if (autoRefreshDropdownRef.value && !autoRefreshDropdownRef.value.contains(target)) {
    showAutoRefreshDropdown.value = false
  }
}

onMounted(() => document.addEventListener('click', handleClickOutside))
onUnmounted(() => document.removeEventListener('click', handleClickOutside))
</script>
