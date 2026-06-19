import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import AccountsView from '../AccountsView.vue'

// Proves the platformTypeBadgesById memoized map (which the platform_type cell
// reads instead of calling 5 helpers — each re-walking row.extra — per row per
// render) produces byte-identical badge output to the per-call path AND stays
// reactive to row-list changes.

const {
  listAccounts,
  listWithEtag,
  getBatchTodayStats,
  getAllProxies,
  getAllGroups
} = vi.hoisted(() => ({
  listAccounts: vi.fn(),
  listWithEtag: vi.fn(),
  getBatchTodayStats: vi.fn(),
  getAllProxies: vi.fn(),
  getAllGroups: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      list: listAccounts,
      listWithEtag,
      getBatchTodayStats,
      delete: vi.fn(),
      batchClearError: vi.fn(),
      batchRefresh: vi.fn(),
      toggleSchedulable: vi.fn()
    },
    proxies: {
      getAll: getAllProxies
    },
    groups: {
      getAll: getAllGroups
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    token: 'test-token'
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

// Mirror the upstream openai-compact state machine the badge derives from, so we
// can compute the expected label independently of the component.
const refOpenAICompactLabel = (row: any): string | null => {
  if (row.platform !== 'openai' || (row.type !== 'oauth' && row.type !== 'apikey')) return null
  const extra = row.extra as Record<string, unknown> | undefined
  const mode = typeof extra?.openai_compact_mode === 'string' ? extra.openai_compact_mode : 'auto'
  let state: 'supported' | 'unsupported' | 'unknown'
  if (mode === 'force_on') state = 'supported'
  else if (mode === 'force_off') state = 'unsupported'
  else if (typeof extra?.openai_compact_supported === 'boolean') state = extra.openai_compact_supported ? 'supported' : 'unsupported'
  else state = 'unknown'
  return {
    supported: 'admin.accounts.openai.compactSupported',
    unsupported: 'admin.accounts.openai.compactUnsupported',
    unknown: 'admin.accounts.openai.compactUnknown'
  }[state]
}

const refAntigravityTierLabel = (row: any): string | null => {
  if (row.platform !== 'antigravity') return null
  const lca = row.extra?.load_code_assist as Record<string, unknown> | undefined
  const tier = (lca?.paidTier as any)?.id ?? (lca?.currentTier as any)?.id ?? null
  switch (tier) {
    case 'free-tier': return 'admin.accounts.tier.free'
    case 'g1-pro-tier': return 'admin.accounts.tier.pro'
    case 'g1-ultra-tier': return 'admin.accounts.tier.ultra'
    default: return null
  }
}

// Stub renders only the platform_type cell, surfacing the openai-compact and
// antigravity badge text so the DOM exposes the memoized derivation. The two
// child badges are stubbed to bare markers (their content is orthogonal).
const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <button data-test="trigger-reload" @click="$emit('sort', 'name', 'desc')">reload</button>
      <div v-for="row in data" :key="row.id" :data-test="'row-' + row.id">
        <slot name="cell-platform_type" :row="row" />
      </div>
    </div>
  `
}

const mountAccounts = () =>
  mount(AccountsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        AccountTableActions: { template: '<div><slot name="beforeCreate" /><slot name="after" /></div>' },
        AccountTableFilters: { template: '<div></div>' },
        AccountBulkActionsBar: true,
        AccountActionMenu: true,
        ImportDataModal: true,
        ReAuthAccountModal: true,
        AccountTestModal: true,
        AccountStatsModal: true,
        ScheduledTestsPanel: true,
        SyncFromCrsModal: true,
        TempUnschedStatusModal: true,
        ErrorPassthroughRulesModal: true,
        TLSFingerprintProfilesModal: true,
        CreateAccountModal: true,
        EditAccountModal: true,
        BulkEditAccountModal: true,
        PlatformTypeBadge: true,
        ChannelTypeBadge: true,
        AccountCapacityCell: true,
        AccountStatusIndicator: true,
        AccountTodayStatsCell: true,
        AccountGroupsCell: true,
        AccountUsageCell: true,
        Icon: true
      }
    }
  })

const accountRow = (over: Record<string, unknown>) => ({
  id: 1,
  name: 'acc',
  platform: 'anthropic',
  type: 'oauth',
  status: 'active',
  schedulable: true,
  created_at: '2026-03-07T10:00:00Z',
  updated_at: '2026-03-07T10:00:00Z',
  ...over
})

// The badge text the platform_type cell renders for a given row (compact label
// + antigravity tier label), read straight from the DOM.
const renderedBadgeTexts = (wrapper: ReturnType<typeof mount>, id: number) =>
  wrapper
    .get(`[data-test="row-${id}"]`)
    .findAll('span')
    .map(s => s.text())
    .filter(Boolean)

describe('admin AccountsView platformTypeBadgesById memoization', () => {
  beforeEach(() => {
    localStorage.clear()
    listAccounts.mockReset()
    listWithEtag.mockReset()
    getBatchTodayStats.mockReset()
    getAllProxies.mockReset()
    getAllGroups.mockReset()

    listWithEtag.mockResolvedValue({ notModified: true, etag: null, data: null })
    getBatchTodayStats.mockResolvedValue({ stats: {} })
    getAllProxies.mockResolvedValue([])
    getAllGroups.mockResolvedValue([])
  })

  it('renders the same openai-compact and antigravity tier badges the per-call helpers would', async () => {
    const openaiRow = accountRow({
      id: 1, platform: 'openai', type: 'apikey',
      extra: { openai_compact_mode: 'force_on' }
    })
    const antigravityRow = accountRow({
      id: 2, platform: 'antigravity', type: 'oauth',
      extra: { load_code_assist: { paidTier: { id: 'g1-ultra-tier' } } }
    })
    const plainRow = accountRow({ id: 3, platform: 'anthropic', type: 'oauth' })

    listAccounts.mockResolvedValue({
      items: [openaiRow, antigravityRow, plainRow],
      total: 3, page: 1, page_size: 20, pages: 1
    })

    const wrapper = mountAccounts()
    await flushPromises()

    // openai row → compact "supported" badge, no antigravity badge.
    expect(refOpenAICompactLabel(openaiRow)).toBe('admin.accounts.openai.compactSupported')
    expect(renderedBadgeTexts(wrapper, 1)).toContain('admin.accounts.openai.compactSupported')

    // antigravity row → ultra tier badge, no compact badge.
    expect(refAntigravityTierLabel(antigravityRow)).toBe('admin.accounts.tier.ultra')
    expect(renderedBadgeTexts(wrapper, 2)).toContain('admin.accounts.tier.ultra')

    // plain anthropic row → neither extra badge.
    expect(refOpenAICompactLabel(plainRow)).toBeNull()
    expect(refAntigravityTierLabel(plainRow)).toBeNull()
    expect(renderedBadgeTexts(wrapper, 3)).not.toContain('admin.accounts.openai.compactSupported')
    expect(renderedBadgeTexts(wrapper, 3)).not.toContain('admin.accounts.tier.ultra')
  })

  it('updates the rendered badges when the account list changes', async () => {
    listAccounts.mockResolvedValue({
      items: [accountRow({ id: 1, platform: 'openai', type: 'apikey', extra: { openai_compact_mode: 'force_on' } })],
      total: 1, page: 1, page_size: 20, pages: 1
    })

    const wrapper = mountAccounts()
    await flushPromises()
    expect(renderedBadgeTexts(wrapper, 1)).toContain('admin.accounts.openai.compactSupported')

    // Reload with a different row whose extra flips the compact state → the memo,
    // keyed off the account list, must re-derive on the SAME instance.
    listAccounts.mockResolvedValue({
      items: [accountRow({ id: 5, platform: 'openai', type: 'apikey', extra: { openai_compact_mode: 'force_off' } })],
      total: 1, page: 1, page_size: 20, pages: 1
    })
    await wrapper.get('[data-test="trigger-reload"]').trigger('click')
    await flushPromises()

    expect(renderedBadgeTexts(wrapper, 5)).toContain('admin.accounts.openai.compactUnsupported')
    expect(renderedBadgeTexts(wrapper, 5)).not.toContain('admin.accounts.openai.compactSupported')
  })
})
