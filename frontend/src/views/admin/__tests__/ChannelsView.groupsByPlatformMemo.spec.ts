import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup } from '@/types'
import ChannelsView from '../ChannelsView.vue'

// Proves getGroupsForPlatform — now backed by the groupsByPlatform memoized map
// instead of re-filtering allGroups on every render/keystroke — renders exactly
// the platform's groups (byte-identical to the old filter) AND stays reactive to
// allGroups changes when the dialog is reopened.

const {
  listChannels,
  getAllGroups,
  getWebSearchEmulationConfig
} = vi.hoisted(() => ({
  listChannels: vi.fn(),
  getAllGroups: vi.fn(),
  getWebSearchEmulationConfig: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      list: listChannels,
      create: vi.fn(),
      update: vi.fn(),
      remove: vi.fn(),
      syncPricingModels: vi.fn()
    },
    groups: {
      getAll: getAllGroups
    },
    settings: {
      getWebSearchEmulationConfig
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn()
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

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, _params?: unknown, fallback?: string) => fallback ?? key
    })
  }
})

const makeGroup = (over: Partial<AdminGroup>): AdminGroup => ({
  id: 0,
  name: 'g',
  platform: 'anthropic',
  status: 'active',
  subscription_type: 'standard',
  is_exclusive: false,
  rate_multiplier: 1,
  account_count: 0,
  ...over
} as unknown as AdminGroup)

// Reference = the exact per-call filter getGroupsForPlatform replaced.
const refGroupsForPlatform = (groups: AdminGroup[], platform: string) =>
  groups.filter(g => g.platform === platform)

const mountChannels = () =>
  mount(ChannelsView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: { props: ['columns', 'data'], template: '<div></div>' },
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        // BaseDialog renders its default + footer slots only when shown,
        // mirroring prod (the cancel button lives in the footer slot).
        BaseDialog: {
          props: ['show'],
          template: '<div v-if="show" data-test="dialog"><slot /><slot name="footer" /></div>'
        },
        PlatformIcon: true,
        Toggle: true,
        PricingEntryCard: true,
        Icon: true
      }
    }
  })

// Enable a platform section and switch to its tab, then read the rendered group
// checkbox labels (the v-for over getGroupsForPlatform).
const openPlatformTab = async (wrapper: ReturnType<typeof mount>, platformKey: string) => {
  await wrapper.get('button.btn-primary').trigger('click') // openCreateDialog
  await flushPromises()
  // The platform-enable checkbox is the one inside the label whose span text is
  // `admin.groups.platforms.<platform>` (NOT the "restrict models" checkbox).
  const platformLabel = wrapper
    .get('[data-test="dialog"]')
    .findAll('label')
    .find(l => l.text().includes(`admin.groups.platforms.${platformKey}`))
  if (!platformLabel) throw new Error(`platform toggle for ${platformKey} not found`)
  await platformLabel.get('input[type="checkbox"]').trigger('change') // togglePlatform
  await flushPromises()
  // Click the now-rendered platform tab to make its content visible.
  const tab = wrapper
    .get('[data-test="dialog"]')
    .findAll('button.channel-tab')
    .find(b => b.text().includes(`admin.groups.platforms.${platformKey}`))
  if (tab) await tab.trigger('click')
  await flushPromises()
}

const renderedGroupNames = (wrapper: ReturnType<typeof mount>) =>
  wrapper
    .get('[data-test="dialog"]')
    .findAll('label span.font-medium')
    .map(s => s.text())

describe('admin ChannelsView groupsByPlatform memoization', () => {
  beforeEach(() => {
    listChannels.mockReset()
    getAllGroups.mockReset()
    getWebSearchEmulationConfig.mockReset()

    listChannels.mockResolvedValue({ items: [], total: 0, page: 1, page_size: 20, pages: 0 })
    getWebSearchEmulationConfig.mockResolvedValue({ enabled: false })
  })

  it('renders exactly the platform-filtered groups the per-call filter would', async () => {
    const groups = [
      makeGroup({ id: 1, name: 'Anthropic-A', platform: 'anthropic' }),
      makeGroup({ id: 2, name: 'Anthropic-B', platform: 'anthropic' }),
      makeGroup({ id: 3, name: 'OpenAI-X', platform: 'openai' }),
      makeGroup({ id: 4, name: 'Gemini-Y', platform: 'gemini' })
    ]
    getAllGroups.mockResolvedValue(groups)

    const wrapper = mountChannels()
    await flushPromises()
    await openPlatformTab(wrapper, 'anthropic')

    const expected = refGroupsForPlatform(groups, 'anthropic').map(g => g.name)
    expect(expected).toEqual(['Anthropic-A', 'Anthropic-B'])

    const names = renderedGroupNames(wrapper)
    expect(names).toEqual(expected)
    expect(names).not.toContain('OpenAI-X')
    expect(names).not.toContain('Gemini-Y')
  })

  it('reflects an updated allGroups set when the dialog is reopened', async () => {
    getAllGroups.mockResolvedValue([
      makeGroup({ id: 1, name: 'Anthropic-A', platform: 'anthropic' })
    ])

    const wrapper = mountChannels()
    await flushPromises()
    await openPlatformTab(wrapper, 'anthropic')
    expect(renderedGroupNames(wrapper)).toEqual(['Anthropic-A'])

    // close dialog
    await wrapper.get('[data-test="dialog"] button.btn-secondary').trigger('click')
    await flushPromises()

    // allGroups changes upstream; reopening triggers loadGroups() → the memo
    // must re-partition from the new catalog.
    getAllGroups.mockResolvedValue([
      makeGroup({ id: 1, name: 'Anthropic-A', platform: 'anthropic' }),
      makeGroup({ id: 9, name: 'Anthropic-Added', platform: 'anthropic' })
    ])
    await openPlatformTab(wrapper, 'anthropic')

    expect(renderedGroupNames(wrapper)).toEqual(['Anthropic-A', 'Anthropic-Added'])
  })
})
