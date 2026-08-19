import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  getDimensions: vi.fn(),
  getSnapshot: vi.fn(),
  getMatrix: vi.fn(),
  getModels: vi.fn(),
  getErrors: vi.fn(),
  getUsers: vi.fn(),
}))

vi.mock('@/api/channelMonitorV2', () => api)
vi.mock('@/stores/auth', () => ({ useAuthStore: () => ({ isAdmin: false }) }))
vi.mock('@/stores/app', () => ({ useAppStore: () => ({ showError: vi.fn() }) }))
vi.mock('@/utils/featureFlags', () => ({ isChannelMonitorThroughputHidden: () => true }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
  useRouter: () => ({ replace: vi.fn() }),
}))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
      te: () => false,
      locale: { value: 'en' },
    }),
  }
})

import ChannelStatusV2View from '../ChannelStatusV2View.vue'

const FilterMultiSelectStub = defineComponent({
  props: {
    modelValue: { type: Array, default: () => [] },
    label: { type: String, default: '' },
    options: { type: Array, default: () => [] },
  },
  emits: ['update:modelValue'],
  template: `
    <button
      type="button"
      class="filter-stub"
      :data-label="label"
      @click="$emit('update:modelValue', ['google'])"
    >
      <span v-for="option in options" :key="option.value">{{ option.label }}</span>
    </button>
  `,
})

const metric = {
  success_requests: 1,
  error_requests: 0,
  request_count: 1,
  token_count: 1,
  rpm: 1,
  tpm: 1,
  error_rate: 0,
  cache_rate: 0,
  cache_rate_numerator: 0,
  cache_rate_denominator: 1,
  ttft: { sample_count: 1, p50_ms: 10, p95_ms: 10, avg_ms: 10 },
  duration: { sample_count: 1, p50_ms: 20, p95_ms: 20, avg_ms: 20 },
}

const health = {
  overall: 'healthy',
  error_rate: 'healthy',
  ttft: 'healthy',
  cache: 'healthy',
  minimum_sample: 1,
}

const coverage = {
  requested_start: '2026-08-01T00:00:00Z',
  requested_end: '2026-08-01T00:01:00Z',
  coverage_start: '2026-08-01T00:00:00Z',
  data_through: '2026-08-01T00:01:00Z',
  computed_at: '2026-08-01T00:01:00Z',
  aggregation_lag_seconds: 0,
  coverage_complete: true,
  bucket_seconds: 60,
}

describe('ChannelStatusV2 public platform boundary', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    Object.values(api).forEach((mock) => mock.mockReset())
    api.getDimensions.mockResolvedValue({
      platforms: [
        { value: 'gemini', label: 'Gemini', request_count: 2 },
        { value: 'antigravity', label: 'Antigravity', request_count: 3 },
        { value: 'openai', label: 'OpenAI', request_count: 1 },
      ],
      groups: [],
      models: [],
    })
    api.getSnapshot.mockResolvedValue({
      config: { refresh_interval_seconds: 300 },
      coverage,
      metrics: metric,
      health,
      trend: [],
    })
    api.getMatrix.mockResolvedValue({ coverage, group_by: 'platform_group', items: [] })
    api.getModels.mockResolvedValue({
      coverage,
      items: [{ platform: 'antigravity', model: 'vertex-model', metrics: metric, health }],
    })
    api.getErrors.mockResolvedValue({ coverage, items: [] })
    api.getUsers.mockResolvedValue({ coverage, items: [] })
  })

  it('shows one Google option and expands it only at the API request boundary', async () => {
    const wrapper = mount(ChannelStatusV2View, {
      global: {
        stubs: {
          FilterMultiSelect: FilterMultiSelectStub,
          MetricCell: true,
          MonitorTrendChart: true,
          RelayPulseMatrix: true,
          MonitorRankBadge: true,
          Icon: true,
          LoadingSpinner: true,
          Select: true,
        },
      },
    })
    await flushPromises()

    const platformFilter = wrapper.find('[data-label="channelMonitorV2.filters.platform"]')
    expect(platformFilter.text()).toBe('GoogleOpenAI')
    expect(wrapper.text()).toContain('Google')
    expect(wrapper.text()).not.toContain('antigravity')

    await platformFilter.trigger('click')
    await flushPromises()

    expect(api.getSnapshot).toHaveBeenLastCalledWith(
      expect.objectContaining({ platforms: ['gemini', 'antigravity', 'google'] }),
      false,
      expect.any(AbortSignal),
    )

    wrapper.unmount()
  })
})
