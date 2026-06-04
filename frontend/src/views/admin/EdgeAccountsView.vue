<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- Header -->
      <div class="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
        <div>
          <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('admin.edgeAccounts.title') }}</h1>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.edgeAccounts.description') }}</p>
        </div>
        <div class="flex flex-wrap items-center gap-2">
          <label class="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t('admin.edgeAccounts.platformFilter') }}</span>
            <select
              class="input input-sm w-36"
              :value="platform"
              @change="setPlatform(($event.target as HTMLSelectElement).value)"
            >
              <option value="all">{{ t('admin.edgeAccounts.allPlatforms') }}</option>
              <option v-for="p in PLATFORM_OPTIONS" :key="p" :value="p">{{ p }}</option>
            </select>
          </label>
          <span v-if="lastFetchedAt" class="text-xs text-gray-400 dark:text-gray-500">
            {{ t('admin.edgeAccounts.lastFetched') }}: {{ formatDateTime(lastFetchedAt) }}
          </span>
          <button
            type="button"
            class="btn btn-secondary inline-flex items-center gap-2"
            :disabled="loading"
            @click="fetch"
          >
            <Icon name="refresh" size="sm" :class="loading ? 'animate-spin' : ''" />
            {{ t('admin.edgeAccounts.refresh') }}
          </button>
        </div>
      </div>

      <!-- Summary bar -->
      <div v-if="!loading || edges.length" class="flex flex-wrap gap-3 text-sm">
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

      <!-- Empty -->
      <div
        v-else-if="!edges.length"
        class="rounded-lg border border-gray-100 bg-white px-4 py-10 text-center text-sm text-gray-500 dark:border-dark-700 dark:bg-dark-800 dark:text-gray-400"
      >
        {{ t('admin.edgeAccounts.noEdges') }}
      </div>

      <!-- Per-edge sections -->
      <div v-else class="space-y-5">
        <section
          v-for="edge in edges"
          :key="edge.edge_id"
          class="overflow-hidden rounded-lg border border-gray-100 bg-white shadow-sm dark:border-dark-700 dark:bg-dark-800"
        >
          <!-- Edge header -->
          <div class="flex flex-wrap items-center justify-between gap-2 border-b border-gray-100 px-4 py-3 dark:border-dark-700">
            <div class="flex min-w-0 items-center gap-3">
              <span :class="['inline-block h-2.5 w-2.5 flex-shrink-0 rounded-full', edge.ok ? 'bg-green-500' : 'bg-red-500']"></span>
              <span class="font-semibold text-gray-900 dark:text-white">{{ edge.edge_id }}</span>
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
                    <div v-if="acct.error_message" class="mt-0.5 max-w-xs truncate text-xs text-red-500" :title="acct.error_message">
                      {{ acct.error_message }}
                    </div>
                    <!-- temp-unschedulable reason shown inline: the reused AccountStatusIndicator's
                         temp-unsched badge opens an admin modal we don't have here (read-only), so
                         surface the reason passively rather than behind an inert click. -->
                    <div v-if="acct.temp_unschedulable_reason" class="mt-0.5 max-w-xs truncate text-xs text-amber-600 dark:text-amber-400" :title="acct.temp_unschedulable_reason">
                      {{ acct.temp_unschedulable_reason }}
                    </div>
                    <!-- Operator 备注, mirroring the admin accounts page name cell. -->
                    <div v-if="acct.notes" class="mt-0.5 block max-w-xs truncate text-xs text-gray-500 dark:text-gray-400" :title="acct.notes">
                      {{ acct.notes }}
                    </div>
                  </td>
                  <td class="px-4 py-2 align-top text-gray-600 dark:text-gray-300">
                    <span>{{ acct.platform }}</span>
                    <span class="text-gray-400 dark:text-gray-500"> / {{ acct.type }}</span>
                    <span v-if="acct.channel_type" class="text-gray-400 dark:text-gray-500"> · ch{{ acct.channel_type }}</span>
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountCapacityCell :account="toAccountLike(acct)" :today-stats="toWindowStats(acct)" />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountUsageCell
                      :account="toAccountLike(acct)"
                      :today-stats="toWindowStats(acct)"
                      :usage-override="toUsageInfo(acct)"
                    />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountStatusIndicator :account="toAccountLike(acct)" />
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
  </AppLayout>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountUsageCell from '@/components/account/AccountUsageCell.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import { schedulableCount, toAccountLike, toWindowStats, toUsageInfo } from '@/utils/edgeAccounts.tk'
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
  loading,
  error,
  lastFetchedAt,
  okEdges,
  failedEdges,
  totalAccounts,
  fetch,
  setPlatform
} = useTkEdgeAccounts()
// Initial fetch + periodic auto-refresh are owned by useTkEdgeAccounts.
</script>
