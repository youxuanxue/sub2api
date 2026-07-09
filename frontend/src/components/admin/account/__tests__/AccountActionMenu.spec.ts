import { flushPromises, mount } from '@vue/test-utils'
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

function mountMenu(account: Account, extraProps: Record<string, unknown> = {}) {
  return mount(AccountActionMenu, {
    props: {
      show: true,
      account,
      position: { top: 0, left: 0 },
      ...extraProps
    },
    global: {
      // Render Teleport content inline so find() can see it; stub Icon.
      stubs: { Teleport: true, Icon: true }
    }
  })
}

function hasTierItem(account: Account): boolean {
  const wrapper = mountMenu(account)
  try {
    return wrapper.findAll('button').some(b => b.text().includes(TIER_ITEM_KEY))
  } finally {
    wrapper.unmount()
  }
}

function hasRecoverStateItem(account: Account): boolean {
  const wrapper = mountMenu(account)
  try {
    return wrapper.findAll('button').some(b => b.text().includes('admin.accounts.recoverState'))
  } finally {
    wrapper.unmount()
  }
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

  it('hides tier action for anthropic OAuth passthrough accounts', () => {
    expect(
      hasTierItem(makeAccount({
        platform: 'anthropic',
        type: 'oauth',
        extra: { anthropic_oauth_passthrough: true }
      } as Partial<Account>))
    ).toBe(false)
  })

  it('hides tier action for non-anthropic accounts', () => {
    expect(hasTierItem(makeAccount({ platform: 'openai', type: 'oauth' }))).toBe(false)
  })
})

describe('AccountActionMenu — 恢复状态入口', () => {
  it('keeps recover-state visible even when the current row has no recoverable flags', () => {
    expect(hasRecoverStateItem(makeAccount({
      status: 'active',
      rate_limit_reset_at: null,
      overload_until: null,
      temp_unschedulable_until: null
    }))).toBe(true)
  })

  it('emits recover-state with the account payload', async () => {
    const account = makeAccount({ id: 42, status: 'active' })
    const wrapper = mountMenu(account)
    const recoverButton = wrapper.findAll('button').find(b => b.text().includes('admin.accounts.recoverState'))

    expect(recoverButton).toBeDefined()
    await recoverButton!.trigger('click')
    await flushPromises()

    expect(wrapper.emitted('recover-state')?.[0]?.[0]).toMatchObject({ id: 42 })
    expect(wrapper.emitted('close')).toBeTruthy()
  })
})

describe('AccountActionMenu — anchored positioning', () => {
  it('aligns the menu to the trigger rect instead of the pointer position fallback', async () => {
    const anchor = document.createElement('button')
    document.body.appendChild(anchor)
    vi.spyOn(anchor, 'getBoundingClientRect').mockReturnValue({
      left: 500,
      right: 560,
      top: 80,
      bottom: 104,
      width: 60,
      height: 24,
      x: 500,
      y: 80,
      toJSON: () => ({})
    } as DOMRect)

    const originalRaf = window.requestAnimationFrame
    const originalCancelRaf = window.cancelAnimationFrame
    window.requestAnimationFrame = ((cb: FrameRequestCallback) => window.setTimeout(() => cb(Date.now()), 0)) as typeof window.requestAnimationFrame
    window.cancelAnimationFrame = ((handle: number) => window.clearTimeout(handle)) as typeof window.cancelAnimationFrame

    const wrapper = mountMenu(
      makeAccount({ status: 'active' }),
      {
        anchor,
        position: { top: 999, left: 999 }
      }
    )

    try {
      await new Promise(resolve => window.setTimeout(resolve, 0))
      await flushPromises()

      const style = wrapper.get('.action-menu-content').attributes('style')
      expect(style).toContain('left: 352px')
      expect(style).toContain('top: 108px')
    } finally {
      wrapper.unmount()
      anchor.remove()
      window.requestAnimationFrame = originalRaf
      window.cancelAnimationFrame = originalCancelRaf
    }
  })
})
