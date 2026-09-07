import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { useCursorAuthorization } from '../useCursorAuthorization.tk'
import { cursorAPI, type CursorAuthorization } from '@/api/admin/cursor.tk'

vi.mock('@/api/admin/cursor.tk', () => ({ cursorAPI: { start: vi.fn(), status: vi.fn(), cancel: vi.fn(), save: vi.fn() } }))
const pending: CursorAuthorization = { id: 'session-test', state: 'pending', authorization_url: 'https://cursor.com/loginDeepControl', expires_at: '2026-12-01' }
let scope: ReturnType<typeof effectScope>
function setup() { scope = effectScope(); return scope.run(() => useCursorAuthorization(key => key))! }
beforeEach(() => { vi.resetAllMocks(); vi.useFakeTimers(); vi.mocked(cursorAPI.cancel).mockResolvedValue(); vi.mocked(cursorAPI.start).mockResolvedValue({ ...pending }) })
afterEach(() => { scope?.stop(); vi.useRealTimers() })

describe('Cursor authorization lifecycle', () => {
  it('cancels a session arriving after the modal has closed', async () => {
    let resolve!: (value: CursorAuthorization) => void
    vi.mocked(cursorAPI.start).mockReturnValue(new Promise(done => { resolve = done }))
    const auth = setup()
    const start = auth.start()
    await Promise.resolve()
    await auth.cancel()
    resolve(pending)
    await start
    expect(auth.session.value).toBeNull()
    expect(cursorAPI.cancel).toHaveBeenCalledWith(pending.id)
    expect(cursorAPI.status).not.toHaveBeenCalled()
  })
  it('ignores a stale poll after cancellation', async () => {
    let resolve!: (value: CursorAuthorization) => void
    vi.mocked(cursorAPI.status).mockReturnValue(new Promise(done => { resolve = done }))
    const auth = setup()
    await auth.start()
    await vi.advanceTimersByTimeAsync(1500)
    await auth.cancel()
    resolve({ ...pending, state: 'authorized' })
    await Promise.resolve()
    expect(auth.session.value).toBeNull()
  })
  it('reuses the import idempotency key after a failed save', async () => {
    vi.mocked(cursorAPI.status).mockResolvedValue({ ...pending, state: 'authorized' })
    vi.mocked(cursorAPI.save).mockRejectedValueOnce(new Error('network')).mockResolvedValueOnce({ id: 7, name: 'Cursor' })
    const auth = setup()
    await auth.start()
    await vi.advanceTimersByTimeAsync(1500)
    expect(await auth.save('Cursor', [2])).toBe(false)
    expect(await auth.save('Cursor', [2])).toBe(true)
    expect(vi.mocked(cursorAPI.save).mock.calls[0][1]).toBe(vi.mocked(cursorAPI.save).mock.calls[1][1])
    expect(auth.session.value).toBeNull()
  })
  it('rejects and cancels an authorization link from an unexpected origin', async () => {
    vi.mocked(cursorAPI.start).mockResolvedValue({ ...pending, authorization_url: 'https://example.com/login' })
    const auth = setup()
    await auth.start()
    expect(auth.session.value).toBeNull()
    expect(cursorAPI.cancel).toHaveBeenCalledWith(pending.id)
    expect(auth.error.value).toBe('admin.accounts.cursor.startFailed')
  })
})
