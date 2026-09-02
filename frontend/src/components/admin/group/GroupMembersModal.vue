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

      <div class="flex flex-wrap items-center gap-2">
        <input
          v-model="memberSearch"
          type="search"
          class="input w-full sm:w-64"
          :placeholder="t('admin.groups.members.searchMembers')"
          @input="scheduleLoadMembers"
        />
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="loadingMembers"
          @click="loadMembers"
        >
          {{ t('common.refresh') }}
        </button>
        <button
          type="button"
          class="btn btn-danger"
          :disabled="selectedIds.length === 0 || removing"
          @click="removeSelected"
        >
          {{ t('admin.groups.members.removeSelected', { count: selectedIds.length }) }}
        </button>
      </div>

      <div class="overflow-x-auto rounded-lg border border-gray-200 dark:border-dark-600">
        <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-600">
          <thead class="bg-gray-50 dark:bg-dark-800">
            <tr>
              <th class="px-3 py-2 text-left">
                <input
                  type="checkbox"
                  :checked="allSelected"
                  :disabled="members.length === 0"
                  @change="toggleSelectAll"
                />
              </th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colName') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colPlatform') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colType') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colStatus') }}</th>
              <th class="px-3 py-2 text-left font-medium">{{ t('admin.groups.members.colSchedulable') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-if="loadingMembers">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500">
                {{ t('common.loading') }}
              </td>
            </tr>
            <tr v-else-if="members.length === 0">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500">
                {{ t('admin.groups.members.empty') }}
              </td>
            </tr>
            <tr
              v-for="row in members"
              :key="row.id"
              class="hover:bg-gray-50 dark:hover:bg-dark-800/60"
            >
              <td class="px-3 py-2">
                <input
                  type="checkbox"
                  :checked="selectedIds.includes(row.id)"
                  @change="toggleSelect(row.id)"
                />
              </td>
              <td class="px-3 py-2 font-medium text-gray-900 dark:text-gray-100">{{ row.name }}</td>
              <td class="px-3 py-2">{{ row.platform }}</td>
              <td class="px-3 py-2">{{ row.type }}</td>
              <td class="px-3 py-2">{{ row.status }}</td>
              <td class="px-3 py-2">{{ row.schedulable ? t('common.yes') : t('common.no') }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="flex items-center justify-between text-sm text-gray-500">
        <span>{{ t('admin.groups.members.total', { count: total }) }}</span>
        <div class="flex gap-2">
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="page <= 1 || loadingMembers"
            @click="page--; loadMembers()"
          >
            {{ t('common.previous') }}
          </button>
          <button
            type="button"
            class="btn btn-secondary btn-sm"
            :disabled="page * pageSize >= total || loadingMembers"
            @click="page++; loadMembers()"
          >
            {{ t('common.next') }}
          </button>
        </div>
      </div>

      <div class="border-t border-gray-200 pt-4 dark:border-dark-600">
        <h4 class="mb-2 text-sm font-medium text-gray-900 dark:text-gray-100">
          {{ t('admin.groups.members.addTitle') }}
        </h4>
        <p class="mb-2 text-xs text-gray-500 dark:text-gray-400">
          {{ addHint }}
        </p>
        <div class="flex flex-wrap gap-2">
          <input
            v-model="candidateSearch"
            type="search"
            class="input w-full sm:w-64"
            :placeholder="t('admin.groups.members.searchCandidates')"
            @input="scheduleLoadCandidates"
          />
          <button
            type="button"
            class="btn btn-primary"
            :disabled="pendingAddIds.length === 0 || adding"
            @click="() => addSelected()"
          >
            {{ t('admin.groups.members.addSelected', { count: pendingAddIds.length }) }}
          </button>
        </div>
        <div class="mt-2 max-h-48 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
          <label
            v-for="acc in candidates"
            :key="acc.id"
            class="flex cursor-pointer items-center gap-2 border-b border-gray-100 px-3 py-2 text-sm last:border-0 dark:border-dark-700"
          >
            <input
              type="checkbox"
              :checked="pendingAddIds.includes(acc.id)"
              :disabled="memberIdSet.has(acc.id)"
              @change="togglePendingAdd(acc.id)"
            />
            <span class="font-medium">{{ acc.name }}</span>
            <span class="text-gray-500">{{ acc.platform }} / {{ acc.type }}</span>
            <span v-if="memberIdSet.has(acc.id)" class="text-xs text-emerald-600">
              {{ t('admin.groups.members.alreadyBound') }}
            </span>
          </label>
          <div v-if="!loadingCandidates && candidates.length === 0" class="px-3 py-4 text-center text-sm text-gray-500">
            {{ t('admin.groups.members.noCandidates') }}
          </div>
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
import type { AdminGroup, Account } from '@/types'
import { useAppStore } from '@/stores/app'

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

const members = ref<GroupMemberAccount[]>([])
const total = ref(0)
const page = ref(1)
const pageSize = 20
const memberSearch = ref('')
const loadingMembers = ref(false)
const selectedIds = ref<number[]>([])
const removing = ref(false)

const candidates = ref<Account[]>([])
const candidateSearch = ref('')
const loadingCandidates = ref(false)
const pendingAddIds = ref<number[]>([])
const adding = ref(false)

let memberTimer: ReturnType<typeof setTimeout> | null = null
let candidateTimer: ReturnType<typeof setTimeout> | null = null

const memberIdSet = computed(() => new Set(members.value.map((m) => m.id)))
const allSelected = computed(
  () => members.value.length > 0 && members.value.every((m) => selectedIds.value.includes(m.id))
)

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

watch(
  () => [props.show, props.group?.id] as const,
  ([show]) => {
    if (!show || !props.group) return
    page.value = 1
    memberSearch.value = ''
    selectedIds.value = []
    pendingAddIds.value = []
    candidateSearch.value = ''
    void loadMembers()
    void loadCandidates()
  }
)

function scheduleLoadMembers() {
  if (memberTimer) clearTimeout(memberTimer)
  memberTimer = setTimeout(() => {
    page.value = 1
    void loadMembers()
  }, 250)
}

function scheduleLoadCandidates() {
  if (candidateTimer) clearTimeout(candidateTimer)
  candidateTimer = setTimeout(() => {
    void loadCandidates()
  }, 250)
}

async function loadMembers() {
  if (!props.group) return
  loadingMembers.value = true
  try {
    const res = await listGroupAccounts(props.group.id, page.value, pageSize, {
      search: memberSearch.value.trim() || undefined
    })
    members.value = res.items || []
    total.value = res.total || 0
    selectedIds.value = selectedIds.value.filter((id) => members.value.some((m) => m.id === id))
  } catch (err) {
    appStore.showError(err instanceof Error ? err.message : String(err))
  } finally {
    loadingMembers.value = false
  }
}

function candidateSearchFilters(): { search?: string; lite: string; status: string } {
  return {
    search: candidateSearch.value.trim() || undefined,
    lite: '1',
    status: 'active'
  }
}

function isCandidateAllowed(acc: Account): boolean {
  const groupPlatform = props.group?.platform || ''
  if (!groupPlatform) return false
  if (groupPlatform === 'composite') {
    return acc.platform !== 'composite'
  }
  if (acc.platform === groupPlatform) return true
  if (
    (groupPlatform === 'anthropic' || groupPlatform === 'gemini') &&
    acc.platform === 'antigravity' &&
    Boolean((acc.extra as Record<string, unknown> | undefined)?.mixed_scheduling)
  ) {
    return true
  }
  return false
}

async function loadCandidates() {
  if (!props.group) return
  loadingCandidates.value = true
  try {
    const res = await listAccounts(1, 50, candidateSearchFilters())
    candidates.value = (res.items || []).filter(isCandidateAllowed)
  } catch (err) {
    appStore.showError(err instanceof Error ? err.message : String(err))
  } finally {
    loadingCandidates.value = false
  }
}

function toggleSelect(id: number) {
  const idx = selectedIds.value.indexOf(id)
  if (idx >= 0) selectedIds.value.splice(idx, 1)
  else selectedIds.value.push(id)
}

function toggleSelectAll(ev: Event) {
  const checked = (ev.target as HTMLInputElement).checked
  selectedIds.value = checked ? members.value.map((m) => m.id) : []
}

function togglePendingAdd(id: number) {
  if (memberIdSet.value.has(id)) return
  const idx = pendingAddIds.value.indexOf(id)
  if (idx >= 0) pendingAddIds.value.splice(idx, 1)
  else pendingAddIds.value.push(id)
}

async function removeSelected() {
  if (!props.group || selectedIds.value.length === 0) return
  if (!window.confirm(t('admin.groups.members.confirmRemove', { count: selectedIds.value.length }))) {
    return
  }
  removing.value = true
  try {
    await unbindGroupAccounts(props.group.id, [...selectedIds.value])
    appStore.showSuccess(t('admin.groups.members.removeSuccess'))
    selectedIds.value = []
    await loadMembers()
    emit('changed')
  } catch (err) {
    appStore.showError(err instanceof Error ? err.message : String(err))
  } finally {
    removing.value = false
  }
}

async function addSelected(confirmMixed = false) {
  if (!props.group || pendingAddIds.value.length === 0) return
  adding.value = true
  try {
    await bindGroupAccounts(props.group.id, [...pendingAddIds.value], {
      confirmMixedChannelRisk: confirmMixed
    })
    appStore.showSuccess(t('admin.groups.members.addSuccess'))
    pendingAddIds.value = []
    await loadMembers()
    await loadCandidates()
    emit('changed')
  } catch (err: unknown) {
    const error = err as { status?: number; error?: string; message?: string }
    if (!confirmMixed && error.status === 409 && error.error === 'mixed_channel_warning') {
      adding.value = false
      if (window.confirm(error.message || t('admin.groups.members.confirmMixed'))) {
        await addSelected(true)
      }
      return
    }
    appStore.showError(error.message || String(err))
  } finally {
    adding.value = false
  }
}
</script>
