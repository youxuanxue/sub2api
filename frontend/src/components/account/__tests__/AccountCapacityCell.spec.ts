import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountCapacityCell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function makeAccount(overrides: Partial<Account> = {}): Account {
  return {
    id: 9,
    name: 'GPT-pro1',
    platform: 'openai',
    type: 'oauth',
    proxy_id: null,
    concurrency: 42,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    current_concurrency: 0,
    ...overrides
  }
}

describe('AccountCapacityCell', () => {
  it('不展示今日统计 badge（由今日统计列单独承载）', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: makeAccount()
      },
      global: {
        stubs: {
          CapacityBadge: {
            props: ['current', 'max'],
            template: '<div class="capacity-badge">{{ current }} / {{ max }}</div>'
          },
          QuotaBadge: true
        }
      }
    })

    expect(wrapper.text()).toContain('0 / 42')
    expect(wrapper.text()).not.toMatch(/\$/)
    expect(wrapper.find('svg path[d*="M6.75 3v2.25"]').exists()).toBe(false)
  })

  it('仍展示并发与配额类容量指标', () => {
    const wrapper = mount(AccountCapacityCell, {
      props: {
        account: makeAccount({
          type: 'apikey',
          quota_daily_limit: 10,
          quota_daily_used: 3
        })
      },
      global: {
        stubs: {
          CapacityBadge: { template: '<div class="capacity-badge"><slot /></div>' },
          QuotaBadge: { template: '<div class="quota-badge" />' }
        }
      }
    })

    expect(wrapper.find('.capacity-badge').exists()).toBe(true)
    expect(wrapper.find('.quota-badge').exists()).toBe(true)
  })
})
