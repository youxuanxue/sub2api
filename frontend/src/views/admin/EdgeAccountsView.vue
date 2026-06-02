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
                  <th class="px-4 py-2 text-left font-medium">{{ t('admin.edgeAccounts.columns.todayStats') }}</th>
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
                  </td>
                  <td class="px-4 py-2 align-top text-gray-600 dark:text-gray-300">
                    <span>{{ acct.platform }}</span>
                    <span class="text-gray-400 dark:text-gray-500"> / {{ acct.type }}</span>
                    <span v-if="acct.channel_type" class="text-gray-400 dark:text-gray-500"> · ch{{ acct.channel_type }}</span>
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountCapacityCell :account="toAccountLike(acct)" />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <AccountTodayStatsCell :stats="toWindowStats(acct)" />
                  </td>
                  <td class="px-4 py-2 align-top">
                    <span :class="['inline-flex rounded-full px-2 py-0.5 text-xs font-medium', stateBadgeClass(acct)]">
                      {{ accountStateLabel(acct) }}
                    </span>
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
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import AccountCapacityCell from '@/components/account/AccountCapacityCell.vue'
import AccountTodayStatsCell from '@/components/account/AccountTodayStatsCell.vue'
import { formatDateTime, formatRelativeTime } from '@/utils/format'
import { useTkEdgeAccounts } from '@/composables/useTkEdgeAccounts'
import { accountStateLabel, accountStatusVariant, schedulableCount, toAccountLike, toWindowStats } from '@/utils/edgeAccounts.tk'
import type { EdgeAccountSummary } from '@/api/admin/edgeAccounts'

const { t } = useI18n()

const {
  edges,
  loading,
  error,
  lastFetchedAt,
  okEdges,
  failedEdges,
  totalAccounts,
  fetch
} = useTkEdgeAccounts()

const STATE_BADGE_CLASSES: Record<string, string> = {
  success: 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300',
  warning: 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300',
  danger: 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300',
  neutral: 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

function stateBadgeClass(a: EdgeAccountSummary): string {
  return STATE_BADGE_CLASSES[accountStatusVariant(a)] ?? STATE_BADGE_CLASSES.neutral
}

onMounted(fetch)
</script>
