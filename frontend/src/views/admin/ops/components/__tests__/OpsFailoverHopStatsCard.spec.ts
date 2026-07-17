import { describe, it, expect, beforeEach, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsFailoverHopStatsCard from '../OpsFailoverHopStatsCard.vue'

const mockGetFailoverHopStats = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getFailoverHopStats: (...args: any[]) => mockGetFailoverHopStats(...args),
  },
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const SelectStub = defineComponent({
  name: 'SelectControlStub',
  props: {
    modelValue: { type: [String, Number], default: '' },
  },
  emits: ['update:modelValue'],
  template: '<div class="select-stub" />',
})

const EmptyStateStub = defineComponent({
  name: 'EmptyState',
  props: {
    title: { type: String, default: '' },
    description: { type: String, default: '' },
  },
  template: '<div class="empty-state">{{ title }}|{{ description }}</div>',
})

const sampleResponse = {
  time_range: '1d' as const,
  start_time: '2026-06-20T00:00:00Z',
  end_time: '2026-06-21T00:00:00Z',
  platform: 'openai',
  group_id: 7,
  items: [
    {
      account_id: 64,
      account_name: 'cc-gpt-64',
      platform: 'openai',
      recovered_count: 139,
      total_failover_hops: 139,
      total_wasted_attempts: 160,
      avg_failover_hops_per_recovered: 1.0,
    },
  ],
  total: 1,
  top_n: 10,
}

function mountCard(props: Record<string, any> = {}) {
  return mount(OpsFailoverHopStatsCard, {
    props: { refreshToken: 0, ...props },
    global: {
      stubs: { Select: SelectStub, EmptyState: EmptyStateStub },
    },
  })
}

describe('OpsFailoverHopStatsCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('默认加载并透传 platform/group/top_n，支持时间窗口切换', async () => {
    mockGetFailoverHopStats.mockResolvedValue(sampleResponse)

    const wrapper = mountCard({ platformFilter: 'openai', groupIdFilter: 7 })
    await flushPromises()

    expect(mockGetFailoverHopStats).toHaveBeenCalledWith(
      expect.objectContaining({ time_range: '1d', platform: 'openai', group_id: 7, top_n: 10 })
    )

    const selects = wrapper.findAllComponents(SelectStub)
    await selects[0].vm.$emit('update:modelValue', '1h')
    await flushPromises()
    expect(mockGetFailoverHopStats).toHaveBeenCalledWith(
      expect.objectContaining({ time_range: '1h', platform: 'openai', group_id: 7 })
    )
  })

  it('切换 TopN 触发按参数请求', async () => {
    mockGetFailoverHopStats.mockResolvedValue(sampleResponse)
    const wrapper = mountCard()
    await flushPromises()

    const selects = wrapper.findAllComponents(SelectStub)
    await selects[1].vm.$emit('update:modelValue', 50)
    await flushPromises()
    expect(mockGetFailoverHopStats).toHaveBeenCalledWith(expect.objectContaining({ top_n: 50 }))
  })

  it('渲染账号行', async () => {
    mockGetFailoverHopStats.mockResolvedValue(sampleResponse)
    const wrapper = mountCard()
    await flushPromises()
    expect(wrapper.text()).toContain('cc-gpt-64')
    expect(wrapper.find('.max-h-\\[420px\\]').exists()).toBe(true)
  })

  it('空数据显示空态', async () => {
    mockGetFailoverHopStats.mockResolvedValue({ ...sampleResponse, items: [], total: 0 })
    const wrapper = mountCard()
    await flushPromises()
    expect(wrapper.find('.empty-state').exists()).toBe(true)
  })

  it('接口异常显示错误提示', async () => {
    mockGetFailoverHopStats.mockRejectedValue(new Error('加载失败'))
    const wrapper = mountCard()
    await flushPromises()
    expect(wrapper.text()).toContain('加载失败')
  })
})
