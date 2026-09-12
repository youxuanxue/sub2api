<template>
  <div class="flex min-h-screen flex-col items-center justify-center gap-4 bg-gray-50 px-6 text-center dark:bg-dark-900">
    <template v-if="!error">
      <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.edgeAccounts.handoff.signingIn') }}</p>
    </template>
    <template v-else>
      <p role="alert" class="text-sm text-red-600 dark:text-red-400">{{ t('admin.edgeAccounts.handoff.failed') }}</p>
      <button type="button" class="btn btn-secondary" @click="goLogin">{{ t('admin.edgeAccounts.handoff.goLogin') }}</button>
    </template>
  </div>
</template>
<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { persistOAuthTokenContext } from '@/api/auth'
import { HANDOFF_MESSAGE, handoffOrigin, handoffRandom, handoffEncode, handoffProof } from '@/utils/edgeHandoff.tk'

const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()
const error = ref(false)
const controller = new AbortController()
const parent = window.opener
let issuer = ''
let verifier = ''
let attempt = ''
let disposed = false
let exchanging = false
let timer: ReturnType<typeof setTimeout> | undefined
function stop() {
  disposed = true
  verifier = ''
  controller.abort()
  clearTimeout(timer)
  window.removeEventListener('message', receive)
}
function fail() {
  if (disposed) return
  error.value = true
  if (parent && issuer) parent.postMessage({ type: HANDOFF_MESSAGE, phase: 'failed', attempt }, issuer)
  stop()
  window.opener = null
}
function goLogin() { stop(); window.opener = null; void router.replace('/login') }
async function receive(event: MessageEvent) {
  if (disposed || exchanging || !issuer || event.source !== parent || event.origin !== issuer || event.data?.type !== HANDOFF_MESSAGE) return
  const data = event.data
  if (data.phase !== 'code' || data.attempt !== attempt || !handoffProof(data.code)) return
  exchanging = true
  try {
    const response = await fetch('/api/v1/edge/admin-handoff/exchange', {
      method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error', signal: controller.signal,
      headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ code: data.code, attempt, verifier })
    })
    verifier = ''
    if (!response.ok) throw new Error('Exchange failed')
    const { data: pair } = await response.json()
    if (disposed) return
    if (!pair?.access_token || !pair?.refresh_token || !(pair.expires_in > 0)) throw new Error('Invalid session')
    // Existing session owner also loads the Edge user and schedules refresh.
    persistOAuthTokenContext(pair)
    await authStore.setToken(pair.access_token)
    if (disposed) return
    parent.postMessage({ type: HANDOFF_MESSAGE, phase: 'complete', attempt }, issuer)
    stop()
    window.opener = null
    await router.replace('/admin/accounts')
  } catch { fail() }
}
onBeforeUnmount(stop)
onMounted(async () => {
  // Old fragments/queries are never accepted, even if they contain valid tokens.
  const legacy = Boolean(window.location.hash || window.location.search)
  history.replaceState(null, '', window.location.pathname)
  if (legacy || !parent) { fail(); return }
  timer = setTimeout(fail, 55000)
  try {
    const response = await fetch('/api/v1/edge/admin-handoff/configuration', { cache: 'no-store', credentials: 'omit', redirect: 'error', signal: controller.signal })
    if (!response.ok) throw new Error('Unavailable')
    const { data: config } = await response.json()
    if (disposed) return
    issuer = handoffOrigin(config.issuer)
    if (handoffOrigin(config.origin) !== window.location.origin) throw new Error('Wrong Edge')
    verifier = handoffRandom()
    attempt = handoffRandom()
    const challenge = handoffEncode(new Uint8Array(await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))))
    if (disposed) return
    window.addEventListener('message', receive)
    parent.postMessage({ type: HANDOFF_MESSAGE, phase: 'ready', challenge, attempt }, issuer)
  } catch { fail() }
})
</script>
