import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import EdgeAccountsView from '../EdgeAccountsView.vue'
import type { AccountUsageInfo } from '@/types'

const { listWithEtag, edgeGetUsage, prodGetUsage } = vi.hoisted(() => ({
  listWithEtag: vi.fn(),
  edgeGetUsage: vi.fn(),
  prodGetUsage: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: { getUsage: prodGetUsage },
    edgeAccounts: {
      listWithEtag,
      getUsage: edgeGetUsage,
      adminSession: vi.fn()
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError: vi.fn() })
}))

const AccountUsageCellLoaderStub = {
  name: 'AccountUsageCell',
  props: {
    activeUsageLoader: Function
  },
  template: '<div />'
}

describe('EdgeAccountsView usage loader', () => {
  beforeEach(() => {
    localStorage.clear()
    listWithEtag.mockReset()
    edgeGetUsage.mockReset()
    prodGetUsage.mockReset()
    listWithEtag.mockResolvedValue({
      notModified: false,
      etag: 'edge-etag',
      data: {
        platform: 'kiro',
        ts: 1,
        edges: [
          {
            edge_id: 'us3',
            base_url: 'https://api-us3.tokenkey.dev',
            ok: true,
            stub_schedulable: true,
            accounts: [
              {
                id: 6,
                name: 'kiro-us3-real',
                platform: 'kiro',
                type: 'oauth',
                status: 'error',
                schedulable: false,
                is_schedulable: false,
                concurrency: 0,
                priority: 10,
                rate_multiplier: 1,
                created_at: '2026-07-01T00:00:00Z',
                groups: ['kiro'],
                usage: {
                  source: 'passive',
                  updated_at: '2026-07-10T00:00:00Z',
                  kiro: { current: 3337, limit: 10_000, percent: 33.37 }
                }
              }
            ]
          }
        ]
      }
    })
  })

  it('routes the row callback through edgeAccounts.getUsage', async () => {
    const activeUsage: AccountUsageInfo = {
      source: 'active',
      updated_at: '2026-07-15T01:02:03Z',
      five_hour: null,
      seven_day: null,
      seven_day_sonnet: null,
      kiro_usage: { current: 10_000, limit: 10_000, percent: 100 }
    }
    edgeGetUsage.mockResolvedValue(activeUsage)

    const wrapper = mount(EdgeAccountsView, {
      global: {
        stubs: {
          Icon: true,
          AccountCapacityCell: true,
          AccountUsageCell: AccountUsageCellLoaderStub,
          AccountStatusIndicator: true
        }
      }
    })
    await flushPromises()

    const loader = wrapper.findComponent(AccountUsageCellLoaderStub).props(
      'activeUsageLoader'
    ) as () => Promise<AccountUsageInfo>
    await expect(loader()).resolves.toEqual(activeUsage)
    expect(edgeGetUsage).toHaveBeenCalledWith('us3', 6, 'active', true)
    expect(prodGetUsage).not.toHaveBeenCalled()

    wrapper.unmount()
  })
})
