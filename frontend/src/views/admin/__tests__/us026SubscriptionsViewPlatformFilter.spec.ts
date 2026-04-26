import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import SubscriptionsView from '../SubscriptionsView.vue'

const { listSubscriptions, getAllGroups, searchUsers } = vi.hoisted(() => {
  return {
    listSubscriptions: vi.fn(),
    getAllGroups: vi.fn(),
    searchUsers: vi.fn(),
  }
})

vi.mock('@/api/admin', () => ({
  adminAPI: {
    subscriptions: {
      list: listSubscriptions,
    },
    groups: {
      getAll: getAllGroups,
    },
    usage: {
      searchUsers,
    },
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showWarning: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn(),
  }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: {} }),
}))

const AppLayoutStub = { template: '<div><slot /></div>' }
const TablePageLayoutStub = {
  template:
    '<div><slot name="filters" /><slot name="actions" /><slot /><slot name="footer" /></div>',
}

interface CapturedSelectOption {
  value: string
  label: string
}

const capturedSelectInstances: Array<{
  options: CapturedSelectOption[]
  placeholder: string
}> = []

const SelectStub = {
  props: ['modelValue', 'options', 'placeholder'],
  emits: ['update:modelValue', 'change'],
  setup(props: { options: CapturedSelectOption[]; placeholder: string }) {
    capturedSelectInstances.push({
      options: props.options,
      placeholder: props.placeholder,
    })
  },
  template: '<select :data-test-placeholder="placeholder" />',
}

describe('US-026: SubscriptionsView platform filter must include the fifth platform newapi', () => {
  beforeEach(() => {
    listSubscriptions.mockReset()
    getAllGroups.mockReset()
    searchUsers.mockReset()
    capturedSelectInstances.length = 0

    listSubscriptions.mockResolvedValue({
      items: [],
      total: 0,
      pages: 0,
      page: 1,
      page_size: 20,
    })
    getAllGroups.mockResolvedValue([])
    searchUsers.mockResolvedValue([])
  })

  it('includes newapi in the platform filter options (regression: hardcoded 4-platform list silently dropped fifth platform)', async () => {
    mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          TablePageLayout: TablePageLayoutStub,
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          GroupBadge: true,
          GroupOptionItem: true,
          Icon: true,
          Select: SelectStub,
        },
      },
    })

    await flushPromises()

    const platformSelect = capturedSelectInstances.find(
      (s) => s.placeholder === 'admin.subscriptions.allPlatforms',
    )
    expect(
      platformSelect,
      'platform filter Select must mount with allPlatforms placeholder',
    ).toBeDefined()

    const values = platformSelect!.options.map((o) => o.value)
    expect(values).toContain('newapi')
    expect(values).toContain('anthropic')
    expect(values).toContain('openai')
    expect(values).toContain('gemini')
    expect(values).toContain('antigravity')
    expect(values).toContain('')

    const newapiOption = platformSelect!.options.find((o) => o.value === 'newapi')
    expect(newapiOption?.label).toBe('Extension Engine')
  })

  it('exposes exactly the 5 canonical platforms + sentinel "all" entry (no extras, no missing)', async () => {
    mount(SubscriptionsView, {
      global: {
        stubs: {
          AppLayout: AppLayoutStub,
          TablePageLayout: TablePageLayoutStub,
          DataTable: true,
          Pagination: true,
          BaseDialog: true,
          ConfirmDialog: true,
          EmptyState: true,
          GroupBadge: true,
          GroupOptionItem: true,
          Icon: true,
          Select: SelectStub,
        },
      },
    })

    await flushPromises()

    const platformSelect = capturedSelectInstances.find(
      (s) => s.placeholder === 'admin.subscriptions.allPlatforms',
    )
    expect(platformSelect).toBeDefined()
    expect(platformSelect!.options).toHaveLength(6)
    expect(platformSelect!.options.map((o) => o.value)).toEqual([
      '',
      'anthropic',
      'openai',
      'gemini',
      'antigravity',
      'newapi',
    ])
  })
})
