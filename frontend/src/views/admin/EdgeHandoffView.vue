<template>
  <!-- Chrome-less transient landing: no AppLayout. Consumes the handoff token,
       establishes the session on THIS (edge) origin, then redirects. -->
  <div class="flex min-h-screen flex-col items-center justify-center gap-4 bg-gray-50 px-6 text-center dark:bg-dark-900">
    <template v-if="!error">
      <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('admin.edgeAccounts.handoff.signingIn') }}</p>
    </template>
    <template v-else>
      <p class="text-sm text-red-600 dark:text-red-400">{{ t('admin.edgeAccounts.handoff.failed') }}</p>
      <button type="button" class="btn btn-secondary" @click="goLogin">
        {{ t('admin.edgeAccounts.handoff.goLogin') }}
      </button>
    </template>
  </div>
</template>

<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAuthStore } from '@/stores/auth'
import { persistOAuthTokenContext } from '@/api/auth'

const { t } = useI18n()
const router = useRouter()
const authStore = useAuthStore()

const error = ref(false)

/** Only allow same-origin absolute paths as the redirect target (no protocol-
 *  relative "//evil.com" open-redirect, no external URLs). */
function safeNext(raw: string | null): string {
  if (raw && raw.startsWith('/') && !raw.startsWith('//')) return raw
  return '/admin/accounts'
}

function goLogin() {
  void router.replace('/login')
}

onMounted(async () => {
  // The token + refresh_token ride in the URL fragment so they never hit the
  // server / logs. refresh_token + expires_in make the edge session self-renewing
  // (like a normal login) so the operator is not bounced to login mid-task.
  const hash = window.location.hash.startsWith('#') ? window.location.hash.slice(1) : window.location.hash
  const params = new URLSearchParams(hash)
  const token = params.get('tk_session')?.trim() || ''
  const refreshToken = params.get('refresh_token')?.trim() || ''
  const expiresIn = Number.parseInt(params.get('expires_in')?.trim() || '', 10)
  const next = safeNext(params.get('next'))

  // Scrub the fragment immediately, before any await, so the token + refresh_token
  // are not left in the address bar / browser history.
  try {
    history.replaceState(null, '', window.location.pathname + window.location.search)
  } catch {
    // ignore — best effort
  }

  if (!token) {
    error.value = true
    return
  }

  try {
    // Persist refresh_token + expires_at BEFORE setToken: setToken reads them from
    // localStorage and schedules proactive refresh, so the handoff session renews
    // itself instead of hard-expiring. Mirrors the OAuth callback flow.
    persistOAuthTokenContext({
      refresh_token: refreshToken || undefined,
      expires_in: Number.isFinite(expiresIn) && expiresIn > 0 ? expiresIn : undefined
    })
    await authStore.setToken(token)
    await router.replace(next)
  } catch {
    error.value = true
  }
})
</script>
