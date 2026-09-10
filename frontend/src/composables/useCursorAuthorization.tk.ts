import { onScopeDispose, ref } from 'vue'
import { cursorAPI, type CursorAuthorization } from '@/api/admin/cursor.tk'
import { ApiError, isNetworkError } from '@/api/client.tk'

export function useCursorAuthorization(t: (key: string) => string) {
  const session = ref<CursorAuthorization | null>(null)
  const busy = ref(false)
  const error = ref('')
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  const importKeys = new Map<string, string>()

  async function cancel() {
    generation++
    clearTimeout(timer)
    const id = session.value?.id
    session.value = null
    importKeys.clear()
    busy.value = false
    if (id) await cursorAPI.cancel(id).catch(() => undefined)
  }

  async function poll(current: number) {
    if (current !== generation || !session.value) return
    if (!(Date.parse(session.value.expires_at) > Date.now())) {
      error.value = t('admin.accounts.cursor.disconnected')
      return
    }
    try {
      const next = await cursorAPI.status(session.value.id)
      if (current !== generation) return
      session.value = next
      error.value = ''
      if (next.state === 'failed') error.value = t('admin.accounts.cursor.authorizationFailed')
      if (next.state === 'pending') timer = setTimeout(() => void poll(current), 1500)
    } catch (cause) {
      if (current !== generation) return
      const status = cause instanceof ApiError ? cause.status : undefined
      if (isNetworkError(cause) || status === 409 || status === 429 || (status !== undefined && status >= 500)) {
        timer = setTimeout(() => void poll(current), 5000)
      } else {
        error.value = t('admin.accounts.cursor.disconnected')
      }
    }
  }

  async function start() {
    const cancelled = cancel()
    const current = generation
    await cancelled
    if (current !== generation) return
    error.value = ''
    busy.value = true
    try {
      const next = await cursorAPI.start()
      if (current !== generation) {
        await cursorAPI.cancel(next.id).catch(() => undefined)
        return
      }
      const url = new URL(next.authorization_url)
      if (url.protocol !== 'https:' || url.hostname !== 'cursor.com') {
        await cursorAPI.cancel(next.id).catch(() => undefined)
        throw new Error('Invalid authorization URL')
      }
      session.value = next
      timer = setTimeout(() => void poll(current), 1500)
    } catch {
      if (current === generation) error.value = t('admin.accounts.cursor.startFailed')
    } finally {
      if (current === generation) busy.value = false
    }
  }

  async function save(name: string, groupIds: number[], accountId?: number) {
    if (!session.value || session.value.state !== 'authorized' || busy.value) return false
    busy.value = true
    error.value = ''
    try {
      const input = { session_id: session.value.id, name, group_ids: groupIds, account_id: accountId }
      const fingerprint = JSON.stringify(input)
      const key = importKeys.get(fingerprint) || crypto.randomUUID()
      importKeys.set(fingerprint, key)
      await cursorAPI.save(input, key)
      session.value = null
      importKeys.clear()
      return true
    } catch {
      error.value = t('admin.accounts.cursor.saveFailed')
      return false
    } finally {
      busy.value = false
    }
  }

  onScopeDispose(() => { void cancel() })
  return { session, busy, error, start, cancel, save }
}
