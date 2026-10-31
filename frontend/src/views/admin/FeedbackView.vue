<template>
  <div class="space-y-6">
    <div>
      <h1 class="text-2xl font-bold text-gray-900 dark:text-white">{{ t('feedback.adminTitle') }}</h1>
      <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">{{ t('feedback.adminDescription') }}</p>
    </div>
    <div class="space-y-3">
      <div v-if="tickets.length === 0" class="card p-6 text-sm text-gray-500">{{ t('feedback.empty') }}</div>
      <article v-for="ticket in tickets" :key="ticket.id" class="card p-5">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h2 class="font-semibold text-gray-900 dark:text-white">{{ ticket.subject }}</h2>
            <p class="text-xs text-gray-400">{{ ticket.ticket_no }} · user #{{ ticket.user_id }}</p>
          </div>
          <select :value="ticket.status" class="input w-36" @change="handleStatusChange(ticket, $event)">
            <option v-for="status in statuses" :key="status" :value="status">{{ t(`feedback.statuses.${status}`) }}</option>
          </select>
        </div>
        <p class="mt-3 whitespace-pre-wrap text-sm text-gray-600 dark:text-dark-300">{{ ticket.content }}</p>
      </article>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import feedbackAPI, { type FeedbackStatus, type FeedbackTicket } from '@/api/feedback'
import { useAppStore } from '@/stores/app'

defineOptions({ name: 'AdminFeedbackView' })
const { t } = useI18n()
const appStore = useAppStore()
const tickets = ref<FeedbackTicket[]>([])
const statuses: FeedbackStatus[] = ['open', 'processing', 'resolved', 'closed']
async function load() { tickets.value = await feedbackAPI.adminList() }
async function update(ticket: FeedbackTicket, status: FeedbackStatus) {
  try { ticket.status = (await feedbackAPI.adminUpdateStatus(ticket.id, status)).status }
  catch (error: any) { appStore.showError(error?.response?.data?.message || t('feedback.updateFailed')); await load() }
}
function handleStatusChange(ticket: FeedbackTicket, event: Event) {
  update(ticket, (event.target as HTMLSelectElement).value as FeedbackStatus)
}
onMounted(load)
</script>
