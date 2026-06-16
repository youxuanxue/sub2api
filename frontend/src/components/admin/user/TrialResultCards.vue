<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <p class="text-sm text-gray-600 dark:text-gray-300">
        {{ t('admin.users.inviteTrial.resultsDone', { ok: okCount, failed: failedCount }) }}
      </p>
      <button type="button" class="btn btn-secondary btn-sm" @click="copyAll">
        <Icon name="copy" size="sm" class="mr-1" />
        {{ t('admin.users.inviteTrial.copyAll') }}
      </button>
    </div>

    <div
      v-for="(cred, idx) in results"
      :key="idx"
      class="rounded-lg border p-3"
      :class="cred.error
        ? 'border-red-300 bg-red-50 dark:bg-red-900/20 dark:border-red-800'
        : 'border-emerald-300 bg-emerald-50 dark:bg-emerald-900/20 dark:border-emerald-800'"
    >
      <div v-if="cred.error" class="text-sm text-red-700 dark:text-red-300">
        <span class="font-medium">{{ cred.email }}</span> — {{ cred.error }}
      </div>
      <template v-else>
        <div class="flex items-start justify-between gap-3">
          <pre class="whitespace-pre-wrap break-all font-mono text-sm text-gray-800 dark:text-gray-100">{{ cred.card_text }}</pre>
          <button type="button" class="btn btn-secondary btn-sm shrink-0" @click="copyOne(cred.card_text)">
            <Icon name="copy" size="sm" class="mr-1" />
            {{ t('admin.users.inviteTrial.copy') }}
          </button>
        </div>
        <div class="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-gray-500 dark:text-gray-400">
          <span v-if="cred.group_name">{{ cred.group_name }}</span>
          <span v-if="cred.expires_at">{{ t('admin.users.inviteTrial.expiresAt') }}: {{ cred.expires_at }}</span>
          <button
            v-if="cred.api_key"
            type="button"
            class="underline decoration-dotted hover:text-gray-700 dark:hover:text-gray-200"
            @click="copyKey(cred.api_key)"
          >
            {{ t('admin.users.inviteTrial.apiKey') }}: {{ maskKey(cred.api_key) }}
          </button>
        </div>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useClipboard } from '@/composables/useClipboard'
import Icon from '@/components/icons/Icon.vue'
import type { TrialCredential } from '@/api/admin/inviteTrial'

const props = defineProps<{ results: TrialCredential[] }>()
const { t } = useI18n()
const { copyToClipboard } = useClipboard()

const okCount = computed(() => props.results.filter((r) => !r.error).length)
const failedCount = computed(() => props.results.filter((r) => r.error).length)

const copyOne = (text: string) => copyToClipboard(text, t('admin.users.inviteTrial.cardCopied'))
const copyKey = (key: string) => copyToClipboard(key, t('admin.users.inviteTrial.copied'))

const copyAll = () => {
  const text = props.results
    .filter((r) => !r.error)
    .map((r) => r.card_text)
    .join('\n\n')
  copyToClipboard(text, t('admin.users.inviteTrial.allCopied'))
}

const maskKey = (key: string) => (key.length > 12 ? `${key.slice(0, 8)}…${key.slice(-4)}` : key)
</script>
