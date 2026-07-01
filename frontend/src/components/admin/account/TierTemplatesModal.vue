<template>
  <BaseDialog
    :show="show"
    :title="t('admin.tierTemplates.title')"
    width="wide"
    @close="$emit('close')"
  >
    <div class="space-y-4">
      <!-- Banner: tiers are a git projection; UI edits are emergency/local -->
      <div class="rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-800/60 dark:bg-amber-900/20 dark:text-amber-300">
        {{ t('admin.tierTemplates.projectionBanner') }}
      </div>

      <!-- Header -->
      <div class="flex items-center justify-between">
        <p class="text-sm text-gray-500 dark:text-gray-400">
          {{ t('admin.tierTemplates.description') }}
        </p>
        <button @click="openCreate" class="btn btn-primary btn-sm">
          <Icon name="plus" size="sm" class="mr-1" />
          {{ t('admin.tierTemplates.createTier') }}
        </button>
      </div>

      <!-- Tiers Table -->
      <div v-if="loading" class="flex items-center justify-center py-8">
        <Icon name="refresh" size="lg" class="animate-spin text-gray-400" />
      </div>

      <div v-else-if="tiers.length === 0" class="py-8 text-center">
        <div class="mx-auto mb-4 flex h-12 w-12 items-center justify-center rounded-full bg-gray-100 dark:bg-dark-700">
          <Icon name="shield" size="lg" class="text-gray-400" />
        </div>
        <h4 class="mb-1 text-sm font-medium text-gray-900 dark:text-white">
          {{ t('admin.tierTemplates.noTiers') }}
        </h4>
      </div>

      <div v-else class="max-h-96 overflow-auto rounded-lg border border-gray-200 dark:border-dark-600">
        <table class="min-w-full divide-y divide-gray-200 dark:divide-dark-700">
          <thead class="sticky top-0 bg-gray-50 dark:bg-dark-700">
            <tr>
              <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.name') }}
              </th>
              <th class="px-3 py-2 text-right text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.concurrency') }}
              </th>
              <th class="px-3 py-2 text-right text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.baseRpm') }}
              </th>
              <th class="px-3 py-2 text-right text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.maxSessions') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.tlsProfile') }}
              </th>
              <th class="px-3 py-2 text-left text-xs font-medium uppercase text-gray-500 dark:text-gray-400">
                {{ t('admin.tierTemplates.columns.actions') }}
              </th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-200 bg-white dark:divide-dark-700 dark:bg-dark-800">
            <tr v-for="tier in tiers" :key="tier.id" class="hover:bg-gray-50 dark:hover:bg-dark-700">
              <td class="px-3 py-2">
                <div class="font-medium text-gray-900 dark:text-white text-sm uppercase">{{ tier.name }}</div>
                <div v-if="tier.description" class="text-xs text-gray-500 dark:text-gray-400 max-w-xs truncate">
                  {{ tier.description }}
                </div>
              </td>
              <td class="px-3 py-2 text-right text-sm text-gray-700 dark:text-gray-300">{{ tier.concurrency }}</td>
              <td class="px-3 py-2 text-right text-sm text-gray-700 dark:text-gray-300">{{ tier.base_rpm }}</td>
              <td class="px-3 py-2 text-right text-sm text-gray-700 dark:text-gray-300">{{ tier.max_sessions }}</td>
              <td class="px-3 py-2">
                <span v-if="tier.tls_profile_name" class="badge badge-primary text-xs">{{ tier.tls_profile_name }}</span>
                <span v-else class="text-xs text-gray-400 dark:text-gray-600">—</span>
              </td>
              <td class="px-3 py-2">
                <div class="flex items-center gap-1">
                  <button
                    @click="handleEdit(tier)"
                    class="p-1 text-gray-500 hover:text-primary-600 dark:hover:text-primary-400"
                    :title="t('common.edit')"
                  >
                    <Icon name="edit" size="sm" />
                  </button>
                  <button
                    @click="handleDelete(tier)"
                    class="p-1 text-gray-500 hover:text-red-600 dark:hover:text-red-400"
                    :title="t('common.delete')"
                  >
                    <Icon name="trash" size="sm" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end">
        <button @click="$emit('close')" class="btn btn-secondary">
          {{ t('common.close') }}
        </button>
      </div>
    </template>

    <!-- Create/Edit Modal -->
    <BaseDialog
      :show="showCreateModal || showEditModal"
      :title="showEditModal ? t('admin.tierTemplates.editTier') : t('admin.tierTemplates.createTier')"
      width="wide"
      :z-index="60"
      @close="closeFormModal"
    >
      <form @submit.prevent="handleSubmit" class="space-y-4">
        <!-- Basic Info -->
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="input-label">{{ t('admin.tierTemplates.form.name') }}</label>
            <input
              v-model="form.name"
              type="text"
              required
              :disabled="showEditModal"
              class="input"
              :placeholder="'l1'"
            />
          </div>
          <div>
            <label class="input-label">{{ t('admin.tierTemplates.form.description') }}</label>
            <input v-model="form.description" type="text" class="input" />
          </div>
        </div>

        <!-- Scheduling numbers -->
        <div class="grid grid-cols-3 gap-4">
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.concurrency') }}</label>
            <input v-model.number="form.concurrency" type="number" min="0" class="input" />
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.priority') }}</label>
            <input v-model.number="form.priority" type="number" min="0" class="input" />
            <p class="input-hint text-xs">{{ t('admin.tierTemplates.form.priorityHint') }}</p>
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.rateMultiplier') }}</label>
            <input v-model.number="form.rate_multiplier" type="number" step="0.01" min="0" class="input" />
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.baseRpm') }}</label>
            <input v-model.number="form.base_rpm" type="number" min="0" class="input" />
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.maxSessions') }}</label>
            <input v-model.number="form.max_sessions" type="number" min="0" class="input" />
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.rpmStickyBuffer') }}</label>
            <input v-model.number="form.rpm_sticky_buffer" type="number" min="0" class="input" />
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.sessionIdleTimeoutMinutes') }}</label>
            <input v-model.number="form.session_idle_timeout_minutes" type="number" min="0" class="input" />
          </div>
        </div>

        <hr class="border-gray-200 dark:border-dark-600" />

        <!-- Cache TTL override -->
        <div class="flex items-center gap-3">
          <button
            type="button"
            @click="form.cache_ttl_override_enabled = !form.cache_ttl_override_enabled"
            :class="[
              'relative inline-flex h-5 w-9 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
              form.cache_ttl_override_enabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
            ]"
          >
            <span
              :class="[
                'pointer-events-none inline-block h-4 w-4 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
                form.cache_ttl_override_enabled ? 'translate-x-4' : 'translate-x-0'
              ]"
            />
          </button>
          <div class="flex-1">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">
              {{ t('admin.tierTemplates.form.cacheTtlOverrideEnabled') }}
            </span>
          </div>
          <div v-if="form.cache_ttl_override_enabled" class="w-40">
            <input
              v-model="form.cache_ttl_override_target"
              type="text"
              class="input"
              :placeholder="'1h'"
            />
          </div>
        </div>

        <!-- TLS profile binding -->
        <div class="grid grid-cols-2 gap-4">
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.tlsProfileName') }}</label>
            <input
              v-model="form.tls_profile_name"
              type="text"
              class="input"
              :placeholder="'tk_canonical_cc_oauth'"
            />
            <p class="input-hint text-xs">{{ t('admin.tierTemplates.form.tlsProfileNameHint') }}</p>
          </div>
          <div>
            <label class="input-label text-xs">{{ t('admin.tierTemplates.form.tlsProfileId') }}</label>
            <input v-model.number="form.tls_profile_id" type="number" min="0" class="input" />
          </div>
        </div>
      </form>

      <template #footer>
        <div class="flex justify-end gap-3">
          <button @click="closeFormModal" type="button" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button @click="handleSubmit" :disabled="submitting" class="btn btn-primary">
            <Icon v-if="submitting" name="refresh" size="sm" class="mr-1 animate-spin" />
            {{ showEditModal ? t('common.update') : t('common.create') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Delete Confirmation -->
    <ConfirmDialog
      :show="showDeleteDialog"
      :title="t('admin.tierTemplates.deleteTier')"
      :message="t('admin.tierTemplates.deleteConfirmMessage', { name: deletingTier?.name })"
      :confirm-text="t('common.delete')"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmDelete"
      @cancel="showDeleteDialog = false"
    />
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, reactive, watch, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { Tier, TierRequest } from '@/api/admin/tier'
import BaseDialog from '@/components/common/BaseDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  close: []
}>()

// eslint-disable-next-line @typescript-eslint/no-unused-vars
void emit // suppress unused warning - emit is used via $emit in template

const { t } = useI18n()
const appStore = useAppStore()

const tiers = ref<Tier[]>([])
const loading = ref(false)
const submitting = ref(false)
const showCreateModal = ref(false)
const showEditModal = ref(false)
const showDeleteDialog = ref(false)
const editingTier = ref<Tier | null>(null)
const deletingTier = ref<Tier | null>(null)

const emptyForm = (): TierRequest => ({
  name: '',
  description: null,
  concurrency: 0,
  priority: 0,
  rate_multiplier: 1,
  base_rpm: 0,
  max_sessions: 0,
  rpm_sticky_buffer: 0,
  session_idle_timeout_minutes: 0,
  cache_ttl_override_enabled: false,
  cache_ttl_override_target: null,
  tls_profile_name: null,
  tls_profile_id: null
})

const form = reactive<TierRequest>(emptyForm())

watch(() => props.show, (newVal) => {
  if (newVal) {
    loadTiers()
  }
})

const loadTiers = async () => {
  loading.value = true
  try {
    tiers.value = await adminAPI.tiers.list()
  } catch (error) {
    appStore.showError(t('admin.tierTemplates.loadFailed'))
    console.error('Error loading tiers:', error)
  } finally {
    loading.value = false
  }
}

// #900 lazy-mount latch: AccountsView/UsersView create this modal with show already
// true on first open, so the show-watch (no immediate) never fires for the initial
// mount. Mirror the watch's true-branch here for the already-shown case. On reopen the
// component stays mounted (latch) so the watch fires and onMounted does not re-run — no
// double-load on a single open.
onMounted(() => {
  if (props.show) {
    loadTiers()
  }
})

const resetForm = () => {
  Object.assign(form, emptyForm())
}

const openCreate = () => {
  resetForm()
  showCreateModal.value = true
}

const closeFormModal = () => {
  showCreateModal.value = false
  showEditModal.value = false
  editingTier.value = null
  resetForm()
}

const handleEdit = (tier: Tier) => {
  editingTier.value = tier
  Object.assign(form, {
    name: tier.name,
    description: tier.description,
    concurrency: tier.concurrency,
    priority: tier.priority,
    rate_multiplier: tier.rate_multiplier,
    base_rpm: tier.base_rpm,
    max_sessions: tier.max_sessions,
    rpm_sticky_buffer: tier.rpm_sticky_buffer,
    session_idle_timeout_minutes: tier.session_idle_timeout_minutes,
    cache_ttl_override_enabled: tier.cache_ttl_override_enabled,
    cache_ttl_override_target: tier.cache_ttl_override_target,
    tls_profile_name: tier.tls_profile_name,
    tls_profile_id: tier.tls_profile_id
  })
  showEditModal.value = true
}

const handleDelete = (tier: Tier) => {
  deletingTier.value = tier
  showDeleteDialog.value = true
}

const handleSubmit = async () => {
  if (!form.name.trim()) {
    appStore.showError(t('admin.tierTemplates.form.name') + ' ' + t('common.required'))
    return
  }

  submitting.value = true
  try {
    const payload: TierRequest = {
      ...form,
      name: form.name.trim(),
      description: form.description?.trim() || null,
      cache_ttl_override_target: form.cache_ttl_override_target?.trim() || null,
      tls_profile_name: form.tls_profile_name?.trim() || null,
      tls_profile_id: form.tls_profile_id || null
    }

    if (showEditModal.value && editingTier.value) {
      await adminAPI.tiers.update(editingTier.value.id, payload)
      appStore.showSuccess(t('admin.tierTemplates.updateSuccess'))
    } else {
      await adminAPI.tiers.create(payload)
      appStore.showSuccess(t('admin.tierTemplates.createSuccess'))
    }

    closeFormModal()
    loadTiers()
  } catch (error: any) {
    appStore.showError(error.response?.data?.message || error.response?.data?.detail || t('admin.tierTemplates.saveFailed'))
    console.error('Error saving tier:', error)
  } finally {
    submitting.value = false
  }
}

const confirmDelete = async () => {
  if (!deletingTier.value) return

  try {
    await adminAPI.tiers.delete(deletingTier.value.id)
    appStore.showSuccess(t('admin.tierTemplates.deleteSuccess'))
    showDeleteDialog.value = false
    deletingTier.value = null
    loadTiers()
  } catch (error: any) {
    appStore.showError(error.response?.data?.message || error.response?.data?.detail || t('admin.tierTemplates.deleteFailed'))
    console.error('Error deleting tier:', error)
  }
}
</script>
