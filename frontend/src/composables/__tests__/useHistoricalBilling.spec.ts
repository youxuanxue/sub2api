import { defineComponent, effectScope, h, KeepAlive, nextTick, ref } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { useHistoricalBilling } from '../useHistoricalBilling'
import { getHistoricalBilling } from '@/api/historicalBilling'
import type { HistoricalBillingReport } from '@/api/historicalBilling'
vi.mock('@/api/historicalBilling', () => ({ getHistoricalBilling: vi.fn() }))
const response = (id: number) => ({ user_id: id, start_date: '2026-06-04', end_date: '2026-10-29', platforms: ['', 'openai'], rows: [], totals: { actual_cost: '1.0000000001' } }) as unknown as HistoricalBillingReport

describe('historical bill query lifecycle', () => {
  beforeEach(() => vi.clearAllMocks())
  it('cancels cached pages and refreshes the same selection on return', async () => {
    const resolvers: Array<(result: HistoricalBillingReport) => void> = []
    vi.mocked(getHistoricalBilling).mockImplementation(() => new Promise(resolve => resolvers.push(resolve)))
    let state!: ReturnType<typeof useHistoricalBilling>
    const visible = ref(true)
    const Page = defineComponent({ setup() {
      state = useHistoricalBilling(false, ref(undefined))
      return () => h('div')
    } })
    const wrapper = mount(defineComponent({ setup: () => () => h(KeepAlive, null, {
      default: () => visible.value ? h(Page) : null,
    }) }))
    state.expanded.value = true
    state.platform.value = 'openai'
    const pending = resolvers.at(-1)!
    const signal = vi.mocked(getHistoricalBilling).mock.calls.at(-1)![1].signal!
    visible.value = false
    await nextTick()
    expect(signal.aborted).toBe(true)
    pending(response(16))
    await flushPromises()
    expect(state.report.value).toBeNull()
    const calls = vi.mocked(getHistoricalBilling).mock.calls.length
    visible.value = true
    await nextTick()
    expect(getHistoricalBilling).toHaveBeenCalledTimes(calls + 1)
    expect(getHistoricalBilling).toHaveBeenLastCalledWith({ granularity: 'day', platform: 'openai' }, expect.anything())
    resolvers.at(-1)!(response(16))
    await flushPromises()
    expect(state.report.value?.user_id).toBe(16)
    wrapper.unmount()
  })
  it('loads only on expansion and ignores stale results after tenant changes', async () => {
    const resolvers: Array<(result: HistoricalBillingReport) => void> = []
    vi.mocked(getHistoricalBilling).mockImplementation(() => new Promise(resolve => resolvers.push(resolve)))
    const scope = effectScope()
    const userId = ref<number | undefined>(16)
    const state = scope.run(() => useHistoricalBilling(true, userId))!
    expect(getHistoricalBilling).not.toHaveBeenCalled()
    state.expanded.value = true
    expect(getHistoricalBilling).toHaveBeenLastCalledWith({ granularity: 'day' }, expect.objectContaining({ admin: true, userId: 16 }))
    userId.value = 17
    expect(state.report.value).toBeNull()
    resolvers.at(-1)!(response(17))
    await flushPromises()
    resolvers[0](response(16))
    await flushPromises()
    expect(state.report.value?.user_id).toBe(17)
    expect(state.startDate.value).toBe('2026-06-04')
    scope.stop()
  })
  it('clears stale charges on failure and retries the exact selection', async () => {
    vi.mocked(getHistoricalBilling).mockResolvedValue(response(16))
    const scope = effectScope()
    const state = scope.run(() => useHistoricalBilling(false, ref(undefined)))!
    state.expanded.value = true
    await flushPromises()
    state.platform.value = '' // unnamed platform is different from all
    await flushPromises()
    vi.mocked(getHistoricalBilling).mockRejectedValue(new Error('timeout'))
    await state.reload()
    expect(state.report.value).toBeNull()
    expect(state.failed.value).toBe(true)
    expect(getHistoricalBilling).toHaveBeenLastCalledWith({ granularity: 'day', platform: '' }, expect.objectContaining({ admin: false }))
    vi.mocked(getHistoricalBilling).mockResolvedValue(response(16))
    await state.reload()
    expect(state.failed.value).toBe(false)
    expect(state.report.value?.totals.actual_cost).toBe('1.0000000001')
    state.range.value = 'custom'
    state.startDate.value = '2026-10-30'
    expect(state.invalidRange.value).toBe(true)
    expect(state.report.value).toBeNull()
    state.expanded.value = false
    expect(state.loading.value).toBe(false)
    scope.stop()
  })
})
