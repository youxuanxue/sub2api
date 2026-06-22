<template>
  <BaseDialog :show="show" :title="t('admin.users.inviteTrial.title')" width="wide" @close="$emit('close')">
    <!-- Results view -->
    <div v-if="results.length > 0">
      <TrialResultCards :results="results" />
    </div>

    <!-- Form view -->
    <form v-else id="invite-trial-form" class="space-y-5" @submit.prevent="onSubmit">
      <!-- Plan source -->
      <div>
        <label class="input-label">{{ t('admin.users.inviteTrial.plan') }}</label>
        <select v-model="form.presetName" class="input" @change="onPresetChange">
          <option value="">{{ t('admin.users.inviteTrial.customPlan') }}</option>
          <option v-for="p in presets" :key="p.name" :value="p.name">{{ p.name }}</option>
        </select>
        <p v-if="presets.length === 0" class="input-hint">{{ t('admin.users.inviteTrial.noPresets') }}</p>
      </div>

      <!-- Plan fields (disabled when a preset drives them) -->
      <div>
        <label class="input-label">{{ t('admin.users.inviteTrial.group') }}</label>
        <select v-model.number="form.groupId" :disabled="usingPreset" class="input">
          <option :value="null" disabled>{{ t('admin.users.inviteTrial.selectGroup') }}</option>
          <option v-for="g in subscriptionGroups" :key="g.id" :value="g.id">
            {{ g.name }}
          </option>
        </select>
      </div>

      <div class="grid grid-cols-2 gap-4">
        <div>
          <label class="input-label">{{ t('admin.users.inviteTrial.validityDays') }}</label>
          <input v-model.number="form.validityDays" :disabled="usingPreset" type="number" min="1" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.inviteTrial.balance') }}</label>
          <input v-model.number="form.balance" :disabled="usingPreset" type="number" step="any" min="0" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.inviteTrial.concurrency') }}</label>
          <input v-model.number="form.concurrency" :disabled="usingPreset" type="number" min="1" class="input" />
        </div>
        <div>
          <label class="input-label">{{ t('admin.users.inviteTrial.rate') }}</label>
          <input v-model.number="rateModel" :disabled="usingPreset" type="number" step="any" min="0" class="input" />
        </div>
      </div>

      <!-- Recipients -->
      <div>
        <label class="input-label">{{ t('admin.users.inviteTrial.recipients') }}</label>
        <textarea
          v-model="form.recipients"
          rows="3"
          class="input font-mono text-sm"
          :placeholder="t('admin.users.inviteTrial.recipientsPlaceholder')"
        />
        <p class="input-hint">{{ t('admin.users.inviteTrial.recipientsHint') }}</p>
      </div>

      <div class="grid grid-cols-2 gap-4">
        <div>
          <label class="input-label">{{ t('admin.users.inviteTrial.autoCount') }}</label>
          <input v-model.number="form.autoCount" type="number" min="0" class="input" />
          <p class="input-hint">{{ t('admin.users.inviteTrial.autoCountHint') }}</p>
        </div>
        <div class="flex items-end pb-1">
          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.issueKey" type="checkbox" class="checkbox" />
            {{ t('admin.users.inviteTrial.issueKey') }}
          </label>
        </div>
      </div>

      <!-- Save as preset (custom mode only) -->
      <div v-if="!usingPreset && form.groupId" class="flex items-end gap-2">
        <div class="flex-1">
          <label class="input-label">{{ t('admin.users.inviteTrial.savePresetName') }}</label>
          <input v-model="savePresetName" type="text" class="input" />
        </div>
        <button type="button" class="btn btn-secondary" :disabled="!savePresetName.trim()" @click="onSavePreset">
          {{ t('admin.users.inviteTrial.savePreset') }}
        </button>
      </div>

      <p v-if="errorText" class="text-sm text-red-600 dark:text-red-400">{{ errorText }}</p>
    </form>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">{{ t('common.close') }}</button>
        <button v-if="results.length > 0" type="button" class="btn btn-primary" @click="createMore">
          {{ t('admin.users.inviteTrial.createMore') }}
        </button>
        <button
          v-else
          type="submit"
          form="invite-trial-form"
          :disabled="submitting"
          class="btn btn-primary"
        >
          {{ submitting ? t('admin.users.inviteTrial.submitting') : t('admin.users.inviteTrial.submit') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { useTkInviteTrial } from '@/composables/useTkInviteTrial'
import { unknownToErrorMessage } from '@/utils/authError'
import BaseDialog from '@/components/common/BaseDialog.vue'
import TrialResultCards from './TrialResultCards.vue'

interface SeedConfig {
  groupId?: number | null
  balance?: number
  concurrency?: number
  rpmLimit?: number
  rate?: number | null
}

const props = defineProps<{ show: boolean; seed?: SeedConfig | null }>()
const emit = defineEmits(['close', 'success'])
const { t } = useI18n()
const appStore = useAppStore()

const {
  form,
  presets,
  subscriptionGroups,
  results,
  submitting,
  load,
  reset,
  seedFromUser,
  applyPreset,
  validate,
  submit,
  savePreset
} = useTkInviteTrial()

const errorText = ref('')
const savePresetName = ref('')

const usingPreset = computed(() => !!form.presetName)

// Bind the nullable rate to a number input (empty → null = group default).
const rateModel = computed({
  get: () => (form.rate == null ? undefined : form.rate),
  set: (v: number | undefined | '') => {
    form.rate = v === undefined || v === '' || Number.isNaN(v as number) ? null : (v as number)
  }
})

const onShown = async () => {
  errorText.value = ''
  savePresetName.value = ''
  reset()
  await load()
  if (props.seed) seedFromUser(props.seed)
}

watch(
  () => props.show,
  async (open) => {
    if (!open) return
    await onShown()
  }
)

// #900 lazy-mount: AccountsView/UsersView mount this modal with show already
// true, so the show-watch (not { immediate: true } — loaders are const after
// it) never fires on first open. Run the same load when mounted already-shown.
onMounted(() => {
  if (props.show) onShown()
})

const onPresetChange = () => {
  errorText.value = ''
  if (form.presetName) applyPreset(form.presetName)
}

const onSubmit = async () => {
  errorText.value = ''
  const invalid = validate()
  if (invalid) {
    errorText.value = t(`admin.users.inviteTrial.${invalid}`)
    return
  }
  try {
    await submit()
    emit('success') // refresh the user list behind the modal
  } catch (e) {
    // The axios interceptor (api/client.ts) rejects with a flattened plain object
    // { status, code, message, ... }, not an Error — so String(e) rendered
    // "[object Object]" in the modal. Pull the backend message via the helper.
    errorText.value = unknownToErrorMessage(e, t('admin.users.inviteTrial.submitFailed'))
  }
}

const onSavePreset = async () => {
  const name = savePresetName.value.trim()
  if (!name) return
  try {
    await savePreset(name)
    appStore.showSuccess(t('admin.users.inviteTrial.presetSaved'))
    form.presetName = name
  } catch (e) {
    errorText.value = unknownToErrorMessage(e, t('admin.users.inviteTrial.savePresetFailed'))
  }
}

const createMore = () => {
  reset()
  errorText.value = ''
}
</script>
