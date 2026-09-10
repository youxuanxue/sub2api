<template>
  <BaseDialog :show="show" :title="t('admin.accounts.cursor.connect')" :show-close-button="!busy" :close-on-escape="!busy" @close="close">
    <form id="cursor-connect-form" class="space-y-4" @submit.prevent="saveAccount">
      <div>
        <label for="cursor-account-name" class="input-label">{{ t('admin.accounts.accountName') }}</label>
        <input id="cursor-account-name" v-model="name" class="input" required maxlength="100" :disabled="busy" />
      </div>
      <div>
        <label for="cursor-service-group" class="input-label">{{ t('admin.accounts.cursor.group') }}</label>
        <select id="cursor-service-group" v-model="groupId" class="input" required :disabled="busy">
          <option :value="null" disabled>{{ t('admin.accounts.cursor.selectGroup') }}</option>
          <option v-for="group in availableGroups" :key="group.id" :value="group.id">{{ group.name }}</option>
        </select>
        <p v-if="!availableGroups.length" class="input-hint">{{ t('admin.accounts.cursor.noGroups') }}</p>
      </div>
      <div v-if="session" class="space-y-3" role="status">
        <template v-if="session.state === 'pending'">
          <p class="text-sm">{{ t('admin.accounts.cursor.pending') }}</p>
          <a :href="session.authorization_url" target="_blank" rel="noopener noreferrer" class="btn btn-primary">
            <Icon name="externalLink" size="sm" />{{ t('admin.accounts.cursor.open') }}
          </a>
        </template>
        <template v-else-if="session.state === 'authorized'">
          <p class="flex items-center gap-2 text-sm text-green-700 dark:text-green-400"><Icon name="check" size="sm" />{{ session.email || t('admin.accounts.cursor.authorized') }}</p>
          <p class="text-sm">{{ t('admin.accounts.cursor.modelCount', { count: modelCount }) }}</p>
          <p v-if="session.key_expires_at" class="text-sm text-gray-500">{{ t('admin.accounts.cursor.expires', { date: new Date(session.key_expires_at).toLocaleDateString() }) }}</p>
        </template>
      </div>
      <p v-if="error" role="alert" class="break-words text-sm text-red-600">{{ error }}</p>
    </form>
    <template #footer>
      <button class="btn btn-secondary" :disabled="busy" @click="close">{{ t('common.cancel') }}</button>
      <button v-if="session?.state === 'authorized'" type="submit" form="cursor-connect-form" class="btn btn-primary" :disabled="busy || !name.trim() || !groupId">
        <Icon name="check" size="sm" />{{ t('common.save') }}
      </button>
      <button v-else class="btn btn-primary" :disabled="busy" @click="start"><Icon name="externalLink" size="sm" />{{ t('admin.accounts.cursor.authorize') }}</button>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Icon from '@/components/icons/Icon.vue'
import { useCursorAuthorization } from '@/composables/useCursorAuthorization.tk'
import type { Account, AdminGroup } from '@/types'

const props = defineProps<{ show: boolean; groups: AdminGroup[]; account?: Account | null }>()
const emit = defineEmits<{ close: []; saved: [] }>()
const { t } = useI18n()
const { session, busy, error, start, cancel, save } = useCursorAuthorization(t)
const name = ref('Cursor')
const groupId = ref<number | null>(null)
const availableGroups = computed(() => props.groups.filter(group => group.platform === 'newapi'))
const modelCount = computed(() => session.value?.models?.filter(model => !['default', 'auto'].includes(model.id)).length || 0)
watch(() => props.show, show => {
  if (!show) { void cancel(); return }
  name.value = props.account?.name || 'Cursor'
  groupId.value = props.account?.group_ids?.[0] || availableGroups.value.find(group => group.name === 'Cursor')?.id || null
  error.value = ''
}, { immediate: true })
async function close() { if (!busy.value) { await cancel(); emit('close') } }
async function saveAccount() {
  if (groupId.value && await save(name.value.trim(), [groupId.value], props.account?.id)) {
    emit('saved'); emit('close')
  }
}
</script>
