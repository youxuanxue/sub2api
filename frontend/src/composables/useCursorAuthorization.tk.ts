import { onScopeDispose, ref } from 'vue'
import { cursorAPI, type CursorAuthorization } from '@/api/admin/cursor.tk'

export function useCursorAuthorization(t: (key: string) => string) {
  const session = ref<CursorAuthorization | null>(null)
  const busy = ref(false)
  const error = ref('')
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let importKey = ''

  async function cancel() {
    generation++
    clearTimeout(timer)
    const id = session.value?.id
    session.value = null
    busy.value = false
    if (id) await cursorAPI.cancel(id).catch(() => undefined)
  }

  async function poll(current: number) {
    if (current !== generation || !session.value) return
    try {
      const next = await cursorAPI.status(session.value.id)
      if (current !== generation) return
      session.value = next
      if (next.state === 'failed') error.value = t('admin.accounts.cursor.authorizationFailed')
      if (next.state === 'pending') timer = setTimeout(() => void poll(current), 1500)
    } catch {
      if (current === generation) error.value = t('admin.accounts.cursor.disconnected')
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
      importKey = crypto.randomUUID()
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
      await cursorAPI.save({ session_id: session.value.id, name, group_ids: groupIds, account_id: accountId }, importKey)
      session.value = null
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
