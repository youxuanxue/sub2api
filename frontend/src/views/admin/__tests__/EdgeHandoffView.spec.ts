// @vitest-environment-options {"url":"http://127.0.0.1:4312"}
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import EdgeHandoffView from '@/views/admin/EdgeHandoffView.vue'
import { HANDOFF_MESSAGE } from '@/utils/edgeHandoff.tk'
const mocks = vi.hoisted(() => ({ replace: vi.fn(), setToken: vi.fn(), persist: vi.fn() }))
vi.mock('vue-router', () => ({ useRouter: () => ({ replace: mocks.replace }) }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ setToken: mocks.setToken }) }))
vi.mock('@/api/auth', () => ({ persistOAuthTokenContext: mocks.persist }))
const parent = { postMessage: vi.fn() }
const fetchMock = vi.fn()
let wrapper: ReturnType<typeof mount> | undefined
function reply(source: unknown = parent, origin = 'https://prod.example') {
  const ready = parent.postMessage.mock.calls[0][0]
  window.dispatchEvent(new MessageEvent('message', { source: source as Window, origin, data: { type: HANDOFF_MESSAGE, phase: 'code', code: 'A'.repeat(43), attempt: ready.attempt } }))
}
describe('EdgeHandoffView', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.stubGlobal('crypto', webcrypto)
    vi.stubGlobal('fetch', fetchMock)
    Object.defineProperty(window, 'opener', { configurable: true, value: parent, writable: true })
    history.replaceState(null, '', '/admin/edge-handoff')
    fetchMock.mockReset().mockResolvedValue({ ok: true, json: async () => ({ data: { issuer: 'https://prod.example', origin: window.location.origin } }) })
  })
  afterEach(() => { wrapper?.unmount(); vi.unstubAllGlobals() })
  async function ready() {
    wrapper = mount(EdgeHandoffView)
    await vi.waitFor(() => expect(parent.postMessage).toHaveBeenCalled())
    fetchMock.mockResolvedValue({ ok: true, json: async () => ({ data: { access_token: 'edge-access', refresh_token: 'edge-refresh', expires_in: 1800 } }) })
  }
  it('exchanges only on the Edge and installs renewable session before navigating', async () => {
    await ready(); reply(); await flushPromises()
    expect(mocks.persist).toHaveBeenCalledWith({ access_token: 'edge-access', refresh_token: 'edge-refresh', expires_in: 1800 })
    expect(mocks.setToken).toHaveBeenCalledWith('edge-access', expect.any(AbortSignal))
    expect(mocks.persist.mock.invocationCallOrder[0]).toBeLessThan(mocks.setToken.mock.invocationCallOrder[0])
    expect(mocks.replace).toHaveBeenCalledWith('/admin/accounts')
    expect(JSON.stringify(parent.postMessage.mock.calls)).not.toContain('edge-refresh')
    expect(window.opener).toBeNull()
  })
  it('ignores wrong origin and wrong window without consuming code', async () => {
    await ready(); reply(parent, 'https://evil.example'); reply(window)
    await flushPromises(); expect(fetchMock).toHaveBeenCalledTimes(1); expect(mocks.setToken).not.toHaveBeenCalled()
    reply(); await flushPromises(); expect(mocks.setToken).toHaveBeenCalledOnce()
  })
  it.each(['#tk_session=legacy&refresh_token=legacy', '?tk_session=legacy'])('scrubs and rejects legacy credentials %s', async suffix => {
    history.replaceState(null, '', `/admin/edge-handoff${suffix}`)
    wrapper = mount(EdgeHandoffView); await flushPromises()
    expect(location.hash + location.search).toBe(''); expect(fetchMock).not.toHaveBeenCalled(); expect(mocks.setToken).not.toHaveBeenCalled()
    expect(wrapper.get('[role=alert]').text()).toContain('handoff.failed')
  })
  it.each(['unmount', 'pagehide'])('cancels user hydration on %s after exchange', async lifecycle => {
    await ready()
    let resolve!: () => void
    mocks.setToken.mockReturnValueOnce(new Promise<void>(r => { resolve = r }))
    reply(); await flushPromises()
    const signal = mocks.setToken.mock.calls[0][1] as AbortSignal
    expect(signal.aborted).toBe(false)
    if (lifecycle === 'unmount') wrapper?.unmount()
    else window.dispatchEvent(new Event('pagehide'))
    expect(signal.aborted).toBe(true)
    resolve(); await flushPromises()
    expect(mocks.replace).not.toHaveBeenCalled()
    expect(parent.postMessage.mock.calls.some(([data]) => data.phase === 'complete')).toBe(false)
  })
  it('does not persist an exchange response after leaving the page', async () => {
    await ready()
    let resolve!: (value: unknown) => void
    fetchMock.mockReturnValue(new Promise(r => { resolve = r }))
    reply(); wrapper?.unmount()
    resolve({ ok: true, json: async () => ({ data: { access_token: 'late', refresh_token: 'late', expires_in: 1800 } }) })
    await flushPromises(); expect(mocks.persist).not.toHaveBeenCalled(); expect(mocks.setToken).not.toHaveBeenCalled()
  })
})
