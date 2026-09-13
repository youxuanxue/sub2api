import { defineComponent } from 'vue'
import { mount, flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { useEdgeAdminHandoff } from '../useEdgeAdminHandoff.tk'
import { HANDOFF_MESSAGE } from '@/utils/edgeHandoff.tk'
const api = vi.hoisted(() => ({ handoffTarget: vi.fn(), adminSession: vi.fn() }))
vi.mock('@/api/admin/edgeAccounts', () => ({ edgeAccountsAPI: api }))
let state: ReturnType<typeof useEdgeAdminHandoff>
let wrapper: ReturnType<typeof mount>
const tab = { location: { href: '' }, closed: false, close: vi.fn(), postMessage: vi.fn() }
const attempt = 'A'.repeat(43)
function ready(source: unknown = tab, origin = 'https://edge.example') {
  window.dispatchEvent(new MessageEvent('message', { source: source as Window, origin, data: { type: HANDOFF_MESSAGE, phase: 'ready', challenge: attempt, attempt } }))
}
describe('shared Edge handoff lifecycle', () => {
  beforeEach(() => {
    vi.clearAllMocks(); vi.useFakeTimers(); tab.closed = false
    api.handoffTarget.mockResolvedValue({ edge_id: 'e1', handoff_url: 'https://edge.example/admin/edge-handoff', enabled: true })
    api.adminSession.mockResolvedValue({ edge_id: 'e1', code: attempt, attempt })
    vi.spyOn(window, 'open').mockReturnValue(tab as unknown as Window)
    wrapper = mount(defineComponent({ setup() { state = useEdgeAdminHandoff(); return () => null } }))
  })
  afterEach(() => { wrapper.unmount(); vi.useRealTimers(); vi.restoreAllMocks() })
  it('binds messages to exact window and origin and passes only a code', async () => {
    await state.openEdgeManage('e1')
    ready(window); ready(tab, 'https://evil.example'); await flushPromises()
    expect(api.adminSession).not.toHaveBeenCalled()
    ready(); await flushPromises()
    expect(tab.postMessage).toHaveBeenCalledWith({ type: HANDOFF_MESSAGE, phase: 'code', code: attempt, attempt }, 'https://edge.example')
    window.dispatchEvent(new MessageEvent('message', { source: tab as unknown as Window, origin: 'https://edge.example', data: { type: HANDOFF_MESSAGE, phase: 'complete', attempt } }))
    expect(state.managingEdge.value).toBeNull(); expect(state.failedEdge.value).toBeNull()
  })
  it('does not forward a late mint after timeout and retry', async () => {
    let resolve!: (data: unknown) => void
    api.adminSession.mockReturnValue(new Promise(r => { resolve = r }))
    await state.openEdgeManage('e1'); ready(); await flushPromises()
    await vi.advanceTimersByTimeAsync(60000)
    expect(state.failedEdge.value).toBe('e1')
    await state.openEdgeManage('e1')
    resolve({ edge_id: 'e1', code: attempt, attempt }); await flushPromises()
    expect(tab.postMessage).not.toHaveBeenCalled()
  })
  it('keeps direct login available when popup is blocked or trust is disabled', async () => {
    vi.mocked(window.open).mockReturnValue(null)
    await state.openEdgeManage('e1')
    expect(state.failedEdge.value).toBe('e1'); expect(state.loginURL.value).toBe('https://edge.example/login')
    api.handoffTarget.mockResolvedValue({ edge_id: 'e1', handoff_url: 'https://edge.example/admin/edge-handoff', enabled: false })
    await state.openEdgeManage('e1'); expect(api.adminSession).not.toHaveBeenCalled()
  })
  it('rejects credential-bearing URLs and releases listeners on unmount', async () => {
    api.handoffTarget.mockResolvedValue({ edge_id: 'e1', handoff_url: 'https://edge.example/admin/edge-handoff#tk_session=retired', enabled: true })
    await state.openEdgeManage('e1'); expect(state.loginURL.value).toBe(''); expect(state.failedEdge.value).toBe('e1')
    wrapper.unmount(); ready(); await flushPromises(); expect(api.adminSession).not.toHaveBeenCalled()
  })
})
