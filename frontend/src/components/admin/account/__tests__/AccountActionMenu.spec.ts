import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import AccountActionMenu from '../AccountActionMenu.vue'
import type { Account } from '@/types'

// t() returns the key verbatim so we can locate the "设置 Tier" item by its key.
const TIER_ITEM_KEY = 'admin.accounts.setTierDialog.menuItem'

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

function makeAccount(partial: Partial<Account>): Account {
  return {
    id: 1,
    name: 'acc',
    platform: 'anthropic',
    type: 'oauth',
    ...partial
  } as Account
}

function mountMenu(account: Account) {
  return mount(AccountActionMenu, {
    props: {
      show: true,
      account,
      position: { top: 0, left: 0 }
    },
    global: {
      // Render Teleport content inline so find() can see it; stub Icon.
      stubs: { Teleport: true, Icon: true }
    }
  })
}

function hasTierItem(account: Account): boolean {
  const wrapper = mountMenu(account)
  return wrapper.findAll('button').some(b => b.text().includes(TIER_ITEM_KEY))
}

describe('AccountActionMenu — 设置 Tier gating', () => {
  it('shows tier action for anthropic oauth accounts', () => {
    expect(hasTierItem(makeAccount({ platform: 'anthropic', type: 'oauth' }))).toBe(true)
  })

  it('shows tier action for anthropic setup-token accounts', () => {
    expect(hasTierItem(makeAccount({ platform: 'anthropic', type: 'setup-token' }))).toBe(true)
  })

  it('hides tier action for anthropic api-key mirror stubs (e.g. prod cc-<edge>)', () => {
    // Backend AccountTierService.applyTier rejects api-key accounts; the menu
    // must not offer an action that always errors out.
    expect(hasTierItem(makeAccount({ platform: 'anthropic', type: 'apikey' }))).toBe(false)
  })

  it('hides tier action for non-anthropic accounts', () => {
    expect(hasTierItem(makeAccount({ platform: 'openai', type: 'oauth' }))).toBe(false)
  })
})
