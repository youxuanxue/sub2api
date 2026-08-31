import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

import AccountBulkActionsBar from '../AccountBulkActionsBar.vue'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({
    t: (key: string) => key
  })
}))

describe('AccountBulkActionsBar', () => {
  it('allows selecting all results before any row is selected', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const button = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.selectAllResults')
    )

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('select-all-results')).toHaveLength(1)
  })

  it('preserves the upstream billing probe action from v0.1.166', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [1],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const button = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.probeUpstreamBilling')
    )

    expect(button).toBeDefined()
    await button!.trigger('click')
    expect(wrapper.emitted('probe-upstream-billing')).toHaveLength(1)
  })

  it('keeps bulk writes enabled when the selection contains a supplier-managed account', async () => {
    const wrapper = mount(AccountBulkActionsBar, {
      props: {
        selectedIds: [1, 2],
        totalResults: 45,
        selectingAll: false,
        allResultsSelected: false
      }
    })

    const deleteButton = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.delete')
    )
    const probeButton = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.probeUpstreamBilling')
    )
    const resetStatusButton = wrapper.findAll('button').find(item =>
      item.text().includes('admin.accounts.bulkActions.resetStatus')
    )

    expect(deleteButton?.attributes('disabled')).toBeUndefined()
    expect(probeButton?.attributes('disabled')).toBeUndefined()
    expect(resetStatusButton?.attributes('disabled')).toBeUndefined()

    await deleteButton!.trigger('click')
    await probeButton!.trigger('click')
    await resetStatusButton!.trigger('click')

    expect(wrapper.emitted('delete')).toHaveLength(1)
    expect(wrapper.emitted('probe-upstream-billing')).toHaveLength(1)
    expect(wrapper.emitted('reset-status')).toHaveLength(1)
  })
})
