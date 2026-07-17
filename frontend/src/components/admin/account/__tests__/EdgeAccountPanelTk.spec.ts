import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import EdgeAccountPanelTk from '../EdgeAccountPanelTk.vue'
import { adminAPI } from '@/api/admin'
import type { Account, AccountUsageInfo } from '@/types'
import type { EdgeAccountSummary, EdgeAccountsResult } from '@/api/admin/edgeAccounts'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn()
  })
}))

function acct(over: Partial<EdgeAccountSummary> = {}): EdgeAccountSummary {
  return {
    id: 8,
    name: 'oh-1-f',
    platform: 'anthropic',
    type: 'oauth',
    status: 'active',
    schedulable: true,
    is_schedulable: true,
    concurrency: 0,
    priority: 1,
    rate_multiplier: 1,
    created_at: '2026-06-09T00:00:00Z',
    groups: ['default'],
    today_stats: {
      requests: 42,
      tokens: 1_000_000,
      cost: 12.34,
      user_cost: 3.21
    },
    usage: {
      source: 'passive',
      five_hour: { utilization: 0.1, resets_at: null },
      seven_day: { utilization: 0.5, resets_at: null }
    },
    ...over
  }
}

function edge(over: Partial<EdgeAccountsResult> = {}): EdgeAccountsResult {
  return {
    edge_id: 'us3',
    base_url: 'https://api-us3.tokenkey.dev',
    ok: true,
    stub_schedulable: true,
    stub_groups: [],
    edge_group: 'default',
    accounts: [acct()],
    ...over
  }
}

const stub: Account = {
  id: 52,
  name: 'cc-us3',
  platform: 'anthropic',
  type: 'apikey',
  status: 'active',
  schedulable: true,
  edge_id: 'us3',
  concurrency: 0,
  priority: 1,
  rate_multiplier: 1,
  created_at: '2026-06-09T00:00:00Z'
} as Account

const AccountUsageCellLoaderStub = {
  name: 'AccountUsageCell',
  props: {
    activeUsageLoader: Function
  },
  template: '<div />'
}

describe('EdgeAccountPanelTk', () => {
  it('renders edge sub-table columns aligned with the main accounts list', () => {
    const wrapper = mount(EdgeAccountPanelTk, {
      props: {
        stub,
        edge: edge()
      },
      global: {
        stubs: {
          Icon: true,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountTodayStatsCell: true,
          AccountUsageCell: true,
          AccountStatusIndicator: true,
          EdgeAccountActionMenuTk: true
        }
      }
    })

    const headers = wrapper.findAll('thead th').map((th) => th.text())
    expect(headers).toEqual([
      'admin.accounts.columns.name',
      'admin.accounts.columns.platformType',
      'admin.accounts.columns.capacity',
      'admin.accounts.columns.status',
      'admin.accounts.columns.schedulable',
      'admin.accounts.columns.todayStats',
      'admin.accounts.columns.groups',
      'admin.accounts.columns.usageWindows',
      'admin.accounts.columns.priority',
      'admin.accounts.edgePanel.actions'
    ])
  })

  it('does not render a last-used column in the edge sub-table', () => {
    const wrapper = mount(EdgeAccountPanelTk, {
      props: {
        stub,
        edge: edge({
          accounts: [acct({ last_used_at: '2026-06-29T10:00:00Z' })]
        })
      },
      global: {
        stubs: {
          Icon: true,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountTodayStatsCell: true,
          AccountUsageCell: true,
          AccountStatusIndicator: true,
          EdgeAccountActionMenuTk: true
        }
      }
    })

    expect(wrapper.text()).not.toContain('admin.edgeAccounts.columns.lastUsed')
    expect(wrapper.findComponent({ name: 'AccountTodayStatsCell' }).exists()).toBe(true)
  })

  it('injects an active usage loader scoped to the edge and local account ID', async () => {
    const activeUsage: AccountUsageInfo = {
      source: 'active',
      updated_at: '2026-07-15T01:02:03Z',
      five_hour: null,
      seven_day: null,
      seven_day_sonnet: null,
      kiro_usage: { current: 10_000, limit: 10_000, percent: 100 }
    }
    const getUsage = vi.spyOn(adminAPI.edgeAccounts, 'getUsage').mockResolvedValue(activeUsage)
    const wrapper = mount(EdgeAccountPanelTk, {
      props: {
        stub,
        edge: edge({ accounts: [acct({ id: 6, platform: 'kiro' })] })
      },
      global: {
        stubs: {
          Icon: true,
          PlatformTypeBadge: true,
          AccountCapacityCell: true,
          AccountTodayStatsCell: true,
          AccountUsageCell: AccountUsageCellLoaderStub,
          AccountStatusIndicator: true,
          EdgeAccountActionMenuTk: true
        }
      }
    })

    const loader = wrapper.findComponent(AccountUsageCellLoaderStub).props(
      'activeUsageLoader'
    ) as () => Promise<AccountUsageInfo>
    await expect(loader()).resolves.toEqual(activeUsage)
    expect(getUsage).toHaveBeenCalledWith('us3', 6, 'active', true)

    getUsage.mockRestore()
  })
})
