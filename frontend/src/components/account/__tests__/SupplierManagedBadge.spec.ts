import { flushPromises, mount } from '@vue/test-utils'
import { defineComponent } from 'vue'
import { describe, expect, it, vi } from 'vitest'

const { listSources } = vi.hoisted(() => ({
  listSources: vi.fn()
}))

vi.mock('@/api/admin/supplierSources', () => ({
  default: {
    list: listSources
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key === 'admin.accounts.supplierManaged.badge'
        ? '供应源托管'
        : key
    })
  }
})

import SupplierManagedBadge from '../SupplierManagedBadge.vue'

const Host = defineComponent({
  components: { SupplierManagedBadge },
  setup() {
    return {
      accounts: [
        { id: 1, extra: { supplier_source_id: 7 } },
        { id: 2, extra: { supplier_source_id: '7' } },
        { id: 3, extra: { supplier_source_id: 99 } },
        { id: 4, extra: { supplier_source_id: 'invalid' } },
        { id: 5, extra: {} }
      ]
    }
  },
  template: `
    <div>
      <SupplierManagedBadge
        v-for="account in accounts"
        :key="account.id"
        :account="account"
      />
    </div>
  `
})

describe('SupplierManagedBadge', () => {
  it('uses marker presence, maps known sources, falls back safely, and refreshes after remount', async () => {
    listSources.mockResolvedValue([
      {
        id: 7,
        supplier_name: '佳杰',
        channel_name: 'VSTECS',
        endpoint: 'https://example.com/v1',
        base_priority: 100,
        models: [],
        notes: '',
        created_at: '2026-08-28T00:00:00Z',
        updated_at: '2026-08-28T00:00:00Z'
      }
    ])

    const wrapper = mount(Host)
    await flushPromises()

    const badges = wrapper.findAll('[data-testid="supplier-managed-badge"]')
    expect(badges).toHaveLength(4)
    expect(badges[0].text()).toBe('供应源托管 · 佳杰/VSTECS')
    expect(badges[1].text()).toBe('供应源托管 · 佳杰/VSTECS')
    expect(badges[2].text()).toBe('供应源托管 #99')
    expect(badges[3].text()).toBe('供应源托管')
    expect(badges[0].attributes('href')).toBe('/admin/supplier-sources?source_id=7')
    expect(badges[2].attributes('href')).toBe('/admin/supplier-sources?source_id=99')
    expect(badges[3].attributes('href')).toBe('/admin/supplier-sources')
    expect(listSources).toHaveBeenCalledTimes(1)

    wrapper.unmount()
    listSources.mockResolvedValueOnce([
      {
        id: 7,
        supplier_name: '佳杰科技',
        channel_name: 'VSTECS-新合同',
        endpoint: 'https://example.com/v1',
        base_priority: 100,
        models: [],
        notes: '',
        created_at: '2026-08-28T00:00:00Z',
        updated_at: '2026-08-28T01:00:00Z'
      }
    ])

    const remounted = mount(Host)
    await flushPromises()

    expect(remounted.findAll('[data-testid="supplier-managed-badge"]')[0].text())
      .toBe('供应源托管 · 佳杰科技/VSTECS-新合同')
    expect(listSources).toHaveBeenCalledTimes(2)
  })
})
