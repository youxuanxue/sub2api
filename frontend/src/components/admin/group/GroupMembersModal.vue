<template>
  <BaseDialog
    :show="show"
    :title="t('admin.groups.members.title', { name: group?.name || '' })"
    width="extra-wide"
    @close="emit('close')"
  >
    <div class="space-y-4">
      <p class="text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.groups.members.hint') }}
      </p>
      <p class="text-xs text-gray-500 dark:text-gray-400">
        {{ addHint }}
      </p>

      <div class="flex flex-wrap items-center gap-2">
        <input
          v-model="search"
          type="search"
          class="input w-full sm:w-64"
          :placeholder="t('admin.groups.members.searchPlaceholder')"
          @input="scheduleReload"
        />
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="loading"
          @click="reload({ refreshBound: true })"
        >
          {{ t('common.refresh') }}
        </button>
      </div>

      <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
        <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
          <thead class="bg-gray-50 dark:bg-dark-800">
            <tr>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colName') }}</th>
              <th class="px-3 py-2 text-left font-medium">
                <div class="space-y-1">
                  <div>{{ t('admin.groups.members.colPlatform') }}</div>
                  <select
                    v-model="filterPlatform"
                    class="input input-sm w-full min-w-[7rem] text-xs font-normal"
                    @change="page = 1; reload()"
                  >
                    <option value="">{{ t('common.all') }}</option>
                    <option v-for="p in platformOptions" :key="p" :value="p">{{ p }}</option>
                  </select>
                </div>
              </th>
              <th class="px-3 py-2 text-left font-medium">
                <div class="space-y-1">
                  <div>{{ t('admin.groups.members.colType') }}</div>
                  <select
                    v-model="filterType"
                    class="input input-sm w-full min-w-[6rem] text-xs font-normal"
                    @change="page = 1; reload()"
                  >
                    <option value="">{{ t('common.all') }}</option>
                    <option value="oauth">oauth</option>
                    <option value="apikey">apikey</option>
                  </select>
                </div>
              </th>
              <th class="px-3 py-2 text-left font-medium">
                <div class="space-y-1">
                  <div>{{ t('admin.groups.members.colStatus') }}</div>
                  <select
                    v-model="filterStatus"
                    class="input input-sm w-full min-w-[6rem] text-xs font-normal"
                    @change="page = 1; reload()"
                  >
                    <option value="">{{ t('common.all') }}</option>
                    <option value="active">active</option>
                    <option value="error">error</option>
                    <option value="inactive">inactive</option>
                    <option value="schedule_suspended">schedule_suspended</option>
                  </select>
                </div>
              </th>
              <th class="px-3 py-2 text-left font-medium">
                <div class="space-y-1">
                  <div>{{ t('admin.groups.members.colSchedulable') }}</div>
                  <select
                    v-model="filterSchedulable"
                    class="input input-sm w-full min-w-[5rem] text-xs font-normal"
                  >
                    <option value="">{{ t('common.all') }}</option>
                    <option value="yes">{{ t('common.yes') }}</option>
                    <option value="no">{{ t('common.no') }}</option>
                  </select>
                </div>
              </th>
              <th class="px-3 py-2 text-left font-medium">
                <div class="space-y-1">
                  <div>{{ t('admin.groups.members.colBound') }}</div>
                  <select
                    v-model="filterBound"
                    class="input input-sm w-full min-w-[5rem] text-xs font-normal"
                    @change="onBoundFilterChange"
                  >
                    <option value="all">{{ t('common.all') }}</option>
                    <option value="bound">{{ t('admin.groups.members.filterBoundYes') }}</option>
                    <option value="unbound">{{ t('admin.groups.members.filterBoundNo') }}</option>
                  </select>
                </div>
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-if="loading">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500">
                {{ t('common.loading') }}
              </td>
            </tr>
            <tr v-else-if="displayRows.length === 0">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500">
                {{
                  needsPlatformPick
                    ? t('admin.groups.members.pickPlatform')
                    : t('admin.groups.members.emptyFiltered')
                }}
              </td>
            </tr>
            <tr
              v-for="row in displayRows"
              :key="row.id"
              class="hover:bg-gray-50 dark:hover:bg-dark-800/60"
            >
              <td class="px-3 py-2 font-medium text-gray-900 dark:text-gray-100">{{ row.name }}</td>
              <td class="px-3 py-2">{{ row.platform }}</td>
              <td class="px-3 py-2">{{ row.type }}</td>
              <td class="px-3 py-2">{{ row.status }}</td>
              <td class="px-3 py-2">{{ row.schedulable ? t('common.yes') : t('common.no') }}</td>
              <td class="px-3 py-2">
                <button
                  type="button"
                  class="relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:cursor-not-allowed disabled:opacity-50 dark:focus:ring-offset-dark-800"
                  :class="[
                    isBound(row.id)
                      ? 'bg-primary-500 hover:bg-primary-600'
                      : 'bg-gray-200 hover:bg-gray-300 dark:bg-dark-600 dark:hover:bg-dark-500'
                  ]"
                  :disabled="togglingIds.has(row.id) || !isCandidateAllowed(row)"
                  :title="
                    isBound(row.id)
                      ? t('admin.groups.members.boundOn')
                      : t('admin.groups.members.boundOff')
                  "
                  :aria-pressed="isBound(row.id)"
                  @click="toggleBound(row)"
                >
                  <span
                    class="pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out"
                    :class="isBound(row.id) ? 'translate-x-4' : 'translate-x-0'"
                  />
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="flex items-center justify-between text-sm text-gray-500">
        <span>{{ t('admin.groups.members.total', { count: totalLabel }) }}</span>
        <div class="flex gap-2">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="page <= 1 || loading"
            @click="page--; reload()"
          >
            {{ t('admin.groups.members.prevPage') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="!canNext || loading"
            @click="page++; reload()"
          >
            {{ t('admin.groups.members.nextPage') }}
          </button>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import {
  bindGroupAccounts,
  listGroupAccounts,
  unbindGroupAccounts,
  type GroupMemberAccount
} from '@/api/admin/groups'
import { list as listAccounts } from '@/api/admin/accounts'
import { ALLOWED_QUOTA_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AdminGroup, Account } from '@/types'
import { useAppStore } from '@/stores/app'

type Row = Account | GroupMemberAccount

const props = defineProps<{
  show: boolean
  group: AdminGroup | null
}>()

const emit = defineEmits<{
  close: []
  changed: []
}>()

const { t } = useI18n()
const appStore = useAppStore()

const rows = ref<Row[]>([])
const boundIds = ref<Set<number>>(new Set())
const boundIdsReady = ref(false)
const total = ref(0)
const page = ref(1)
const pageSize = 20
const search = ref('')
const loading = ref(false)
const togglingIds = ref<Set<number>>(new Set())

const filterBound = ref<'all' | 'bound' | 'unbound'>('bound')
const filterPlatform = ref('')
const filterType = ref('')
const filterStatus = ref('')
const filterSchedulable = ref('')

let reloadTimer: ReturnType<typeof setTimeout> | null = null

const addHint = computed(() => {
  const platform = props.group?.platform || ''
  if (platform === 'composite') {
    return t('admin.groups.members.addHintComposite')
  }
  if (platform === 'anthropic' || platform === 'gemini') {
    return t('admin.groups.members.addHintMixed')
  }
  return t('admin.groups.members.addHintSamePlatform', { platform })
})

const platformOptions = computed(() => {
  const gp = props.group?.platform || ''
  // SSOT: concrete account platforms (excludes composite).
  if (gp === 'composite') return [...ALLOWED_QUOTA_PLATFORMS]
  if (gp === 'anthropic' || gp === 'gemini') return [gp, 'antigravity']
  return gp ? [gp] : []
})

const displayRows = computed(() => {
  let list = rows.value
  if (filterSchedulable.value === 'yes') {
    list = list.filter((r) => r.schedulable)
  } else if (filterSchedulable.value === 'no') {
    list = list.filter((r) => !r.schedulable)
  }
  return list
})

const totalLabel = computed(() => {
  if (filterSchedulable.value) return displayRows.value.length
  return total.value
})

const canNext = computed(() => {
  if (filterSchedulable.value) return false
  return page.value * pageSize < total.value
})

const needsPlatformPick = computed(() => {
  if (filterBound.value === 'bound') return false
  const gp = props.group?.platform || ''
  if (gp === 'composite' && !filterPlatform.value) return true
  return false
})

watch(
  () => [props.show, props.group?.id] as const,
  ([show]) => {
    if (!show || !props.group) return
    page.value = 1
    search.value = ''
    filterBound.value = 'bound'
    filterPlatform.value = ''
    filterType.value = ''
    filterStatus.value = ''
    filterSchedulable.value = ''
    boundIdsReady.value = false
    void reload({ refreshBound: true })
  }
)

function isBound(id: number) {
  return boundIds.value.has(id)
}

function isCandidateAllowed(acc: Row): boolean {
  const groupPlatform = props.group?.platform || ''
  if (!groupPlatform) return false
  if (groupPlatform === 'composite') {
    return acc.platform !== 'composite'
  }
  if (acc.platform === groupPlatform) return true
  if (
    (groupPlatform === 'anthropic' || groupPlatform === 'gemini') &&
    acc.platform === 'antigravity'
  ) {
    const extra = (acc as Account).extra as Record<string, unknown> | null | undefined
    if (Boolean(extra?.mixed_scheduling) || boundIds.value.has(acc.id)) return true
  }
  if (boundIds.value.has(acc.id)) return true
  return false
}

/** Single concrete platform for eligible-account queries (no multi-platform fan-out). */
function resolveEligiblePlatform(): string | null {
  if (filterPlatform.value) return filterPlatform.value
  const gp = props.group?.platform || ''
  if (!gp || gp === 'composite') return null
  return gp
}

function scheduleReload() {
  if (reloadTimer) clearTimeout(reloadTimer)
  reloadTimer = setTimeout(() => {
    page.value = 1
    void reload()
  }, 250)
}

function onBoundFilterChange() {
  page.value = 1
  void reload()
}

async function refreshBoundIds() {
  if (!props.group) return
  const ids = new Set<number>()
  let p = 1
  const size = 100
  for (;;) {
    const res = await listGroupAccounts(props.group.id, p, size)
    for (const item of res.items || []) ids.add(item.id)
    if (!res.items?.length || ids.size >= (res.total || 0)) break
    p += 1
    if (p > 50) break
  }
  boundIds.value = ids
  boundIdsReady.value = true
}

async function ensureBoundIds(force = false) {
  if (!force && boundIdsReady.value) return
  await refreshBoundIds()
}

async function reload(opts?: { refreshBound?: boolean }) {
  if (!props.group) return
  loading.value = true
  try {
    // Bound-ID set is only needed for unbound/all filtering and toggle state.
    // Refresh on open (and explicit force); reuse across search/filter/page.
    if (opts?.refreshBound || filterBound.value !== 'bound' || !boundIdsReady.value) {
      await ensureBoundIds(Boolean(opts?.refreshBound))
    }

    if (filterBound.value === 'bound') {
      await loadBoundPage()
    } else {
      await loadEligiblePage()
    }
  } catch (err) {
    appStore.showError(err instanceof Error ? err.message : String(err))
  } finally {
    loading.value = false
  }
}

function matchesBoundClientFilters(row: Row): boolean {
  if (filterPlatform.value && row.platform !== filterPlatform.value) return false
  if (filterType.value && row.type !== filterType.value) return false
  return true
}

async function loadBoundPage() {
  if (!props.group) return
  const query = {
    search: search.value.trim() || undefined,
    status: filterStatus.value || undefined
  }
  const needsClientShape = Boolean(filterPlatform.value || filterType.value)

  if (!needsClientShape) {
    const res = await listGroupAccounts(props.group.id, page.value, pageSize, query)
    rows.value = (res.items || []) as Row[]
    total.value = res.total || 0
    return
  }

  // API has no platform/type; walk membership pages, filter, then slice.
  const filtered: Row[] = []
  let p = 1
  const size = 100
  for (;;) {
    const res = await listGroupAccounts(props.group.id, p, size, query)
    for (const item of res.items || []) {
      if (matchesBoundClientFilters(item)) filtered.push(item)
    }
    if (!res.items?.length || p * size >= (res.total || 0)) break
    p += 1
    if (p > 50) break
  }
  const start = (page.value - 1) * pageSize
  rows.value = filtered.slice(start, start + pageSize)
  total.value = filtered.length
}

function accountListFilters(platform: string) {
  return {
    search: search.value.trim() || undefined,
    lite: '1',
    platform,
    ...(filterStatus.value ? { status: filterStatus.value } : {}),
    ...(filterType.value ? { type: filterType.value } : {})
  }
}

async function loadEligiblePage() {
  if (!props.group) return
  const platform = resolveEligiblePlatform()
  if (!platform) {
    rows.value = []
    total.value = 0
    return
  }

  if (filterBound.value === 'unbound') {
    await loadUnboundPage(platform)
    return
  }

  // filterBound === 'all'
  const res = await listAccounts(page.value, pageSize, accountListFilters(platform))
  rows.value = (res.items || []).filter(isCandidateAllowed)
  total.value = res.total || 0
}

/** Walk account pages to fill one page of unbound rows (stable skip by page index). */
async function loadUnboundPage(platform: string) {
  const skip = (page.value - 1) * pageSize
  let skipped = 0
  const collected: Account[] = []
  let p = 1
  let serverTotal = 0
  const maxPages = 40

  while (collected.length < pageSize && p <= maxPages) {
    const res = await listAccounts(p, 50, accountListFilters(platform))
    serverTotal = res.total || 0
    const items = res.items || []
    if (items.length === 0) break
    for (const acc of items) {
      if (!isCandidateAllowed(acc) || boundIds.value.has(acc.id)) continue
      if (skipped < skip) {
        skipped += 1
        continue
      }
      collected.push(acc)
      if (collected.length >= pageSize) break
    }
    if (p * 50 >= serverTotal) break
    p += 1
  }

  rows.value = collected
  // Exact when short page; otherwise enable Next until a short page proves end.
  total.value =
    collected.length < pageSize ? skip + collected.length : skip + pageSize + 1
}

async function toggleBound(row: Row) {
  if (!props.group || togglingIds.value.has(row.id)) return
  const groupID = props.group.id
  const currentlyBound = boundIds.value.has(row.id)

  if (currentlyBound) {
    if (!window.confirm(t('admin.groups.members.confirmUnbindOne', { name: row.name }))) {
      return
    }
  }

  const next = new Set(togglingIds.value)
  next.add(row.id)
  togglingIds.value = next

  try {
    if (currentlyBound) {
      await unbindGroupAccounts(groupID, [row.id])
      const ids = new Set(boundIds.value)
      ids.delete(row.id)
      boundIds.value = ids
      appStore.showSuccess(t('admin.groups.members.removeSuccess'))
    } else {
      await bindOne(row, false)
    }
    emit('changed')
    // Keep row visible; if viewing bound-only and unbound, drop it.
    if (filterBound.value === 'bound' && !boundIds.value.has(row.id)) {
      rows.value = rows.value.filter((r) => r.id !== row.id)
      total.value = Math.max(0, total.value - 1)
    }
  } catch (err) {
    appStore.showError(err instanceof Error ? err.message : String(err))
  } finally {
    const done = new Set(togglingIds.value)
    done.delete(row.id)
    togglingIds.value = done
  }
}

async function bindOne(row: Row, confirmMixed: boolean) {
  if (!props.group) return
  try {
    await bindGroupAccounts(props.group.id, [row.id], {
      confirmMixedChannelRisk: confirmMixed
    })
    const ids = new Set(boundIds.value)
    ids.add(row.id)
    boundIds.value = ids
    appStore.showSuccess(t('admin.groups.members.addSuccess'))
  } catch (err: unknown) {
    const error = err as { status?: number; error?: string; message?: string }
    if (!confirmMixed && error.status === 409 && error.error === 'mixed_channel_warning') {
      if (window.confirm(error.message || t('admin.groups.members.confirmMixed'))) {
        await bindOne(row, true)
      }
      return
    }
    throw err instanceof Error ? err : new Error(error.message || String(err))
  }
}
</script>
