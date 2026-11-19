import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import UserBreakdownSubTable from '../UserBreakdownSubTable.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('UserBreakdownSubTable cost columns', () => {
  const items = [{ user_id: 1, email: 'user@example.com', requests: 2, total_tokens: 500, actual_cost: 1, account_cost: 2, cost: 3 }]
  it('retains admin customer, supplier, and standard costs', () => {
    const wrapper = mount(UserBreakdownSubTable, { props: { items } })
    expect(wrapper.findAll('td')).toHaveLength(6)
    expect(wrapper.text()).toContain('$1.00')
    expect(wrapper.text()).toContain('$2.00')
    expect(wrapper.text()).toContain('$3.00')
  })
  it('hides supplier and standard cost when the parent hides them', () => {
    const wrapper = mount(UserBreakdownSubTable, { props: { items, showAccountCost: false, showStandardCost: false } })
    expect(wrapper.findAll('td')).toHaveLength(4)
    expect(wrapper.text()).toContain('$1.00')
    expect(wrapper.text()).not.toContain('$2.00')
    expect(wrapper.text()).not.toContain('$3.00')
  })
})
