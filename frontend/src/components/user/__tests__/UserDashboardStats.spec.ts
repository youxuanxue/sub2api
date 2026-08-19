/**
 * Regression test for UserDashboardStats.vue
 *
 * PR #1731: the gateway latency card must bind `average_gateway_latency_ms`
 * (auth+routing only), NOT `average_duration_ms` (end-to-end including LLM
 * generation). Rendering the wrong field shows 10s+ LLM time as if it were
 * gateway delay — the exact regression this test prevents.
 */
import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import UserDashboardStats from '../dashboard/UserDashboardStats.vue'

const messages: Record<string, string> = {
  'dashboard.balance': 'Balance',
  'dashboard.apiKeys': 'API Keys',
  'dashboard.todayRequests': 'Today Requests',
  'dashboard.todayCost': 'Today Cost',
  'dashboard.todayTokens': 'Today Tokens',
  'dashboard.totalTokens': 'Total Tokens',
  'dashboard.performance': 'Performance',
  'dashboard.avgGatewayLatency': 'Avg Gateway Latency',
  'dashboard.gatewayAverageTime': "Today's gateway latency",
  'dashboard.input': 'Input',
  'dashboard.output': 'Output',
  'dashboard.actual': 'Actual',
  'common.available': 'Available',
  'common.active': 'active',
  'common.total': 'Total',
}

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, _params?: Record<string, unknown>) => messages[key] ?? key,
    }),
  }
})

vi.mock('@/composables/usePlatformOptions', () => ({
  getPlatformLabel: (p: string) => p,
}))

vi.mock('@/constants/gatewayPlatforms', () => ({
  GATEWAY_PLATFORMS: ['anthropic', 'openai', 'gemini'],
}))

const baseStats = {
  total_api_keys: 2,
  active_api_keys: 1,
  total_requests: 500,
  total_input_tokens: 10000,
  total_output_tokens: 5000,
  total_cache_creation_tokens: 0,
  total_cache_read_tokens: 0,
  total_tokens: 15000,
  total_cost: 1.5,
  total_actual_cost: 1.2,
  today_requests: 50,
  today_input_tokens: 1000,
  today_output_tokens: 500,
  today_cache_creation_tokens: 0,
  today_cache_read_tokens: 0,
  today_tokens: 1500,
  today_cost: 0.15,
  today_actual_cost: 0.12,
  average_duration_ms: 10580, // end-to-end LLM time — must NOT appear
  average_gateway_latency_ms: 42, // gateway auth+routing only — MUST appear
  rpm: 10,
  tpm: 5000,
}

describe('UserDashboardStats', () => {
  it('renders gateway latency (average_gateway_latency_ms), not end-to-end duration', () => {
    const wrapper = mount(UserDashboardStats, {
      props: {
        stats: baseStats,
        balance: 50,
        isSimple: false,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    const text = wrapper.text()

    // Gateway latency card must show the correct label and value
    expect(text).toContain('Avg Gateway Latency')
    expect(text).toContain('42ms')

    // Must NOT render average_duration_ms (10.58s)
    expect(text).not.toContain('10.58s')
  })

  it('formats gateway latency in seconds when >= 1000ms', () => {
    const wrapper = mount(UserDashboardStats, {
      props: {
        stats: { ...baseStats, average_gateway_latency_ms: 1250 },
        balance: 50,
        isSimple: false,
      },
      global: {
        stubs: {
          Icon: true,
        },
      },
    })

    expect(wrapper.text()).toContain('1.25s')
  })
})
