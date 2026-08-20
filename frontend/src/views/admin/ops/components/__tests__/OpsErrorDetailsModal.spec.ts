import { describe, it, expect, beforeEach, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import OpsErrorDetailsModal from '../OpsErrorDetailsModal.vue'
import { OPS_MAX_WINDOW_MS } from '../../utils/opsTimeRange'

const mockListRequestErrors = vi.fn()
const mockListUpstreamErrors = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    listRequestErrors: (...args: any[]) => mockListRequestErrors(...args),
    listUpstreamErrors: (...args: any[]) => mockListUpstreamErrors(...args)
  }
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

const stubs = {
  BaseDialog: { template: '<div><slot /></div>' },
  Select: true,
  OpsErrorLogTable: true
}

async function openModal(props: Record<string, unknown>) {
  const wrapper = mount(OpsErrorDetailsModal, {
    props: {
      show: false,
      timeRange: '1h',
      errorType: 'request',
      ...props
    },
    global: { stubs }
  })
  await wrapper.setProps({ show: true })
  await flushPromises()
  return wrapper
}

describe('OpsErrorDetailsModal', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    mockListRequestErrors.mockResolvedValue({ items: [], total: 0 })
    mockListUpstreamErrors.mockResolvedValue({ items: [], total: 0 })
  })

  it('sends the clamped 30-day window instead of oversized custom props', async () => {
    const end = '2026-08-20T13:37:00.000+08:00'
    const start = '2026-07-21T00:00:00.000+08:00'

    await openModal({
      timeRange: 'custom',
      customStartTime: start,
      customEndTime: end,
      errorType: 'request'
    })

    expect(mockListRequestErrors).toHaveBeenCalled()
    const params = mockListRequestErrors.mock.calls[0][0]
    expect(params.start_time).toBe(new Date(Date.parse(end) - OPS_MAX_WINDOW_MS).toISOString())
    expect(params.end_time).toBe(new Date(end).toISOString())
    expect(params.start_time).not.toBe(start)
    expect(params.time_range).toBeUndefined()
  })

  it('falls back to 1h when a custom range is incomplete', async () => {
    await openModal({
      timeRange: 'custom',
      customStartTime: null,
      customEndTime: null,
      errorType: 'request'
    })

    const params = mockListRequestErrors.mock.calls[0][0]
    expect(params.time_range).toBe('1h')
    expect(params.start_time).toBeUndefined()
    expect(params.end_time).toBeUndefined()
  })
})
