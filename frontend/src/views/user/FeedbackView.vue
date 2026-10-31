<template>
  <div class="mx-auto max-w-4xl space-y-6">
    <div>
      <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('feedback.title') }}</h1>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('feedback.description') }}</p>
    </div>

    <form class="card space-y-4 p-6" @submit.prevent="submit">
      <div class="grid gap-4 sm:grid-cols-2">
        <label class="block">
          <span class="input-label">{{ t('feedback.category') }}</span>
          <select v-model="form.category" class="input mt-1" required>
            <option v-for="category in categories" :key="category" :value="category">
              {{ t(`feedback.categories.${category}`) }}
            </option>
          </select>
        </label>
        <label class="block">
          <span class="input-label">{{ t('feedback.subject') }}</span>
          <input v-model="form.subject" class="input mt-1" maxlength="120" required />
        </label>
      </div>
      <label class="block">
        <span class="input-label">{{ t('feedback.content') }}</span>
        <textarea v-model="form.content" class="input mt-1 min-h-32" maxlength="4000" required />
      </label>
      <div class="flex justify-end">
        <button class="btn btn-primary" type="submit" :disabled="submitting">
          {{ submitting ? '…' : t('feedback.submit') }}
        </button>
      </div>
    </form>

    <div class="space-y-3">
      <div v-if="!loading && tickets.length === 0" class="card p-6 text-sm text-gray-500">{{ t('feedback.empty') }}</div>
      <article v-for="ticket in tickets" :key="ticket.id" class="card p-5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h2 class="font-semibold text-gray-900 dark:text-white">{{ ticket.subject }}</h2>
          <span class="badge badge-gray">{{ t(`feedback.statuses.${ticket.status}`) }}</span>
        </div>
        <p class="mt-2 whitespace-pre-wrap text-sm text-gray-600 dark:text-dark-300">{{ ticket.content }}</p>
        <p class="mt-3 text-xs text-gray-400">{{ t('feedback.ticketNo') }} {{ ticket.ticket_no }} · {{ formatDate(ticket.created_at) }}</p>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import feedbackAPI, { type FeedbackCategory, type FeedbackTicket } from '@/api/feedback'
import { useAppStore } from '@/stores/app'

defineOptions({ name: 'UserFeedbackView' })
const { t } = useI18n()
const appStore = useAppStore()
const categories: FeedbackCategory[] = ['api', 'billing', 'technical', 'other']
const tickets = ref<FeedbackTicket[]>([])
const loading = ref(false)
const submitting = ref(false)
const form = reactive({ category: 'technical' as FeedbackCategory, subject: '', content: '' })

function formatDate(value: string) {
  return new Date(value).toLocaleString()
}
async function load() {
  loading.value = true
  try { tickets.value = await feedbackAPI.list() } finally { loading.value = false }
}
async function submit() {
  submitting.value = true
  try {
    await feedbackAPI.create(form)
    form.subject = ''
    form.content = ''
    appStore.showSuccess(t('feedback.submitted'))
    await load()
  } catch (error: any) {
    appStore.showError(error?.response?.data?.message || error?.message || 'Request failed')
  } finally { submitting.value = false }
}
onMounted(load)
</script>
