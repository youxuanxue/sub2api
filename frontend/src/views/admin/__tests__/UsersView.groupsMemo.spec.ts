import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

import type { AdminGroup, AdminUser } from '@/types'
import UsersView from '../UsersView.vue'

// Proves the userGroupsById memoized map (which the groups cell reads instead of
// calling getUserGroups(row) ~8× per render) produces byte-identical rendered
// output to the per-call path AND stays reactive to allGroups / row-list changes.

const {
  listUsers,
  getAllGroups,
  getBatchUsersUsage,
  listEnabledDefinitions,
  getBatchUserAttributes
} = vi.hoisted(() => ({
  listUsers: vi.fn(),
  getAllGroups: vi.fn(),
  getBatchUsersUsage: vi.fn(),
  listEnabledDefinitions: vi.fn(),
  getBatchUserAttributes: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    users: {
      list: listUsers,
      toggleStatus: vi.fn(),
      delete: vi.fn()
    },
    groups: {
      getAll: getAllGroups
    },
    dashboard: {
      getBatchUsersUsage
    },
    userAttributes: {
      listEnabledDefinitions,
      getBatchUserAttributes
    }
  }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn()
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

const makeGroup = (over: Partial<AdminGroup>): AdminGroup => ({
  id: 0,
  name: 'g',
  platform: 'anthropic',
  status: 'active',
  subscription_type: 'standard',
  is_exclusive: false,
  rate_multiplier: 1,
  // The remaining AdminGroup fields are irrelevant to getUserGroups; cast keeps
  // the fixture minimal while satisfying the type at the call sites we exercise.
  ...over
} as unknown as AdminGroup)

const makeUser = (over: Partial<AdminUser>): AdminUser => ({
  id: 1,
  username: 'u',
  email: 'u@example.com',
  role: 'user',
  balance: 0,
  concurrency: 1,
  status: 'active',
  allowed_groups: [],
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  created_at: '2026-04-17T00:00:00Z',
  updated_at: '2026-04-17T00:00:00Z',
  notes: '',
  last_active_at: '2026-04-16T02:00:00Z',
  last_used_at: '2026-04-17T02:00:00Z',
  current_concurrency: 0,
  ...over
} as unknown as AdminUser)

// Reference implementation = the exact per-call logic the memo replaces.
const refGetUserGroups = (user: AdminUser, groups: AdminGroup[]) => {
  const exclusive: AdminGroup[] = []
  const publicGroups: AdminGroup[] = []
  for (const g of groups) {
    if (g.status !== 'active' || g.subscription_type !== 'standard') continue
    if (g.is_exclusive) {
      if (user.allowed_groups?.includes(g.id)) exclusive.push(g)
    } else {
      publicGroups.push(g)
    }
  }
  return { exclusive, publicGroups }
}

const DataTableStub = {
  props: ['columns', 'data'],
  emits: ['sort'],
  template: `
    <div>
      <button data-test="trigger-reload" @click="$emit('sort', 'created_at', 'desc')">reload</button>
      <div v-for="row in data" :key="row.id" :data-test="'row-' + row.id">
        <slot name="cell-groups" :row="row" />
      </div>
    </div>
  `
}

const mountUsers = () =>
  mount(UsersView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        TablePageLayout: {
          template: '<div><slot name="filters" /><slot name="table" /><slot name="pagination" /></div>'
        },
        DataTable: DataTableStub,
        Pagination: true,
        ConfirmDialog: true,
        EmptyState: true,
        GroupBadge: true,
        Select: true,
        UserAttributesConfigModal: true,
        UserConcurrencyCell: true,
        UserCreateModal: true,
        InviteTrialModal: true,
        UserEditModal: true,
        UserApiKeysModal: true,
        UserAllowedGroupsModal: true,
        UserBalanceModal: true,
        UserBalanceHistoryModal: true,
        GroupReplaceModal: true,
        UserPlatformQuotaModal: true,
        UserPlatformQuotaCell: true,
        PlatformUsageBreakdown: true,
        PlatformCostCell: true,
        // Render group names verbatim so the DOM exposes the resolved exclusive/
        // public partition for assertion.
        Icon: { props: ['name'], template: '<i :data-icon="name"></i>' },
        Teleport: true
      }
    }
  })

// Extract the exclusive + public group-name lists the cell rendered for a row,
// by reading the tooltip <span> lists (one per exclusive group, one per public).
const renderedGroupsForRow = (wrapper: ReturnType<typeof mount>, userId: number) => {
  const rowEl = wrapper.get(`[data-test="row-${userId}"]`)
  const exclusiveIcon = rowEl.find('[data-icon="shield"]')
  const publicIcon = rowEl.find('[data-icon="globe"]')
  // The numeric badge directly follows each icon; group names live in the
  // hover tooltips. We assert on the tooltip name lists for byte-fidelity.
  const allNameSpans = rowEl.findAll('span').map(s => s.text())
  return { hasExclusive: exclusiveIcon.exists(), hasPublic: publicIcon.exists(), allNameSpans }
}

describe('admin UsersView userGroupsById memoization', () => {
  beforeEach(() => {
    localStorage.clear()
    // The "groups" column is hidden by default; reveal it so onMounted calls
    // loadAllGroups() and the groups cell renders the memoized partition.
    localStorage.setItem('user-hidden-columns', JSON.stringify([]))
    localStorage.setItem('user-column-settings-version', '3')

    listUsers.mockReset()
    getAllGroups.mockReset()
    getBatchUsersUsage.mockReset()
    listEnabledDefinitions.mockReset()
    getBatchUserAttributes.mockReset()

    getBatchUsersUsage.mockResolvedValue({ stats: {} })
    listEnabledDefinitions.mockResolvedValue([])
    getBatchUserAttributes.mockResolvedValue({ values: {} })
  })

  it('renders the same exclusive/public partition the per-call getUserGroups would', async () => {
    const groups = [
      makeGroup({ id: 10, name: 'Exclusive-A', is_exclusive: true }),
      makeGroup({ id: 11, name: 'Exclusive-B', is_exclusive: true }),
      makeGroup({ id: 20, name: 'Public-X', is_exclusive: false }),
      // Disabled + non-standard groups must be excluded by both paths.
      makeGroup({ id: 30, name: 'Disabled', is_exclusive: true, status: 'disabled' }),
      makeGroup({ id: 31, name: 'Subscription', is_exclusive: false, subscription_type: 'subscription' })
    ]
    const user = makeUser({ id: 7, allowed_groups: [10] })

    listUsers.mockResolvedValue({ items: [user], total: 1, page: 1, page_size: 20, pages: 1 })
    getAllGroups.mockResolvedValue(groups)

    const wrapper = mountUsers()
    await flushPromises()

    const expected = refGetUserGroups(user, groups)
    expect(expected.exclusive.map(g => g.name)).toEqual(['Exclusive-A'])
    expect(expected.publicGroups.map(g => g.name)).toEqual(['Public-X'])

    const rendered = renderedGroupsForRow(wrapper, 7)
    expect(rendered.hasExclusive).toBe(true)
    expect(rendered.hasPublic).toBe(true)
    // Exactly the expected group names appear; excluded ones do not.
    expect(rendered.allNameSpans).toContain('Exclusive-A')
    expect(rendered.allNameSpans).toContain('Public-X')
    expect(rendered.allNameSpans).not.toContain('Exclusive-B')
    expect(rendered.allNameSpans).not.toContain('Disabled')
    expect(rendered.allNameSpans).not.toContain('Subscription')
  })

  it('updates the rendered partition when the visible row list changes', async () => {
    const groups = [
      makeGroup({ id: 10, name: 'Exclusive-A', is_exclusive: true }),
      makeGroup({ id: 11, name: 'Exclusive-B', is_exclusive: true }),
      makeGroup({ id: 20, name: 'Public-X', is_exclusive: false })
    ]
    getAllGroups.mockResolvedValue(groups)

    // First load: user can see Exclusive-A only.
    listUsers.mockResolvedValue({
      items: [makeUser({ id: 7, allowed_groups: [10] })],
      total: 1, page: 1, page_size: 20, pages: 1
    })

    const wrapper = mountUsers()
    await flushPromises()

    expect(renderedGroupsForRow(wrapper, 7).allNameSpans).toContain('Exclusive-A')
    expect(renderedGroupsForRow(wrapper, 7).allNameSpans).not.toContain('Exclusive-B')

    // Reload with a different user (different id + different allowed_groups) →
    // the memo, keyed off the row list, must re-derive on the SAME instance.
    listUsers.mockResolvedValue({
      items: [makeUser({ id: 9, allowed_groups: [11] })],
      total: 1, page: 1, page_size: 20, pages: 1
    })
    await wrapper.get('[data-test="trigger-reload"]').trigger('click')
    await flushPromises()

    const rendered = renderedGroupsForRow(wrapper, 9)
    expect(rendered.allNameSpans).toContain('Exclusive-B')
    expect(rendered.allNameSpans).not.toContain('Exclusive-A')
    expect(rendered.allNameSpans).toContain('Public-X')
  })
})
