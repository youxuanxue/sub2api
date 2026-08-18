<template>
  <BaseDialog
    :show="show"
    :title="t('keys.useKeyModal.title')"
    width="wide"
    @close="emit('close')"
  >
    <UseKeyGuide
      :api-key="apiKey"
      :api-key-id="apiKeyId"
      :base-url="baseUrl"
      :platform="platform"
      :routing-mode="routingMode"
      :initial-model="initialModel"
      :claude-code-only="claudeCodeOnly"
      :allow-messages-dispatch="allowMessagesDispatch"
      :supported-model-scopes="supportedModelScopes"
    />

    <template #footer>
      <div class="flex justify-end">
        <button class="btn btn-secondary" @click="emit('close')">
          {{ t('common.close') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import UseKeyGuide from '@/components/keys/UseKeyGuide.vue'
import type { GroupPlatform, KeyRoutingMode } from '@/types'

interface Props {
  show: boolean
  apiKey: string
  baseUrl: string
  platform: GroupPlatform | null
  apiKeyId?: number | null
  initialModel?: string | null
  routingMode?: KeyRoutingMode
  claudeCodeOnly?: boolean
  allowMessagesDispatch?: boolean
  supportedModelScopes?: string[]
}

defineProps<Props>()
const emit = defineEmits<{
  close: []
}>()
const { t } = useI18n()
</script>
