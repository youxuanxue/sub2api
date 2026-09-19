<template>
  <div v-if="safeURL" class="flex flex-wrap items-center gap-2 text-xs" :title="t('admin.accounts.verificationLinkHint')" data-testid="google-verification-link">
    <a :href="safeURL" @click.stop target="_blank" rel="noopener noreferrer" class="text-blue-600 hover:underline dark:text-blue-400">
      {{ t('admin.accounts.openVerification') }}
    </a>
    <button type="button" class="text-blue-600 hover:underline dark:text-blue-400" @click.stop="copyToClipboard(safeURL, t('admin.accounts.linkCopied'))">
      {{ copied ? t('admin.accounts.linkCopied') : t('admin.accounts.copyLink') }}
    </button>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useClipboard } from '@/composables/useClipboard'
import { googleVerificationURL } from '@/utils/antigravityRecovery'

const props = defineProps<{ url?: string | null }>()
const { t } = useI18n()
const { copied, copyToClipboard } = useClipboard()
const safeURL = computed(() => googleVerificationURL(props.url))
</script>
