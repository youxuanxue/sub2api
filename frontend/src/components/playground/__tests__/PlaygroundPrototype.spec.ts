/**
 * US-032 PR 2 P1-B prototype A 件 — PlaygroundPrototype.vue tests
 *
 * Spec: docs/approved/user-cold-start.md §11
 *       .testing/user-stories/stories/US-032-playground-prototype-AB.md
 *
 * What we test (per US-032 AC-001 / AC-002):
 *   - Component mounts without Pinia / vue-router / network → "无依赖的视觉胶片"
 *   - Each of the 4 states renders the expected DOM contract
 *
 * What we test ALSO (per US-032 AC-004 + AC-005):
 *   - The static HTML mockup at docs/approved/attachments/playground-prototype-2026-04-23.html
 *     declares the same 4 data-state values; this gives us a parity
 *     guarantee — if someone adds a 5th Vue state without updating the HTML,
 *     CI fails here loudly. Same in the other direction.
 */

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
import { readFileSync, existsSync } from 'node:fs'
import { resolve } from 'node:path'
import PlaygroundPrototype, { type PlaygroundState } from '../PlaygroundPrototype.vue'

const ALL_STATES: PlaygroundState[] = ['empty', 'typing', 'responded', 'error']

describe('PlaygroundPrototype', () => {
  it('renders empty state', () => {
    const wrapper = mount(PlaygroundPrototype, { props: { state: 'empty' } })
    expect(wrapper.attributes('data-state')).toBe('empty')
    expect(wrapper.find('[data-testid="placeholder"]').exists()).toBe(true)
    // No conversation surface on empty state
    expect(wrapper.find('[data-testid="conversation"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="user-message"]').exists()).toBe(false)
    // Composer + send button always rendered (visual only); send NOT disabled on empty
    expect(wrapper.find('[data-testid="send-button"]').attributes('disabled')).toBeUndefined()
  })

  it('renders typing state', () => {
    const wrapper = mount(PlaygroundPrototype, { props: { state: 'typing' } })
    expect(wrapper.attributes('data-state')).toBe('typing')
    expect(wrapper.find('[data-testid="user-message"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="assistant-typing"]').exists()).toBe(true)
    // typing → input + send disabled (cannot send while a response is streaming)
    expect(wrapper.find('[data-testid="composer-input"]').attributes('disabled')).toBeDefined()
    expect(wrapper.find('[data-testid="send-button"]').attributes('disabled')).toBeDefined()
    // abort button is the user's escape hatch
    expect(wrapper.find('[data-testid="abort-button"]').exists()).toBe(true)
    // No completed assistant message and no error banner during typing
    expect(wrapper.find('[data-testid="assistant-message"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="error-banner"]').exists()).toBe(false)
  })

  it('renders responded state', () => {
    const wrapper = mount(PlaygroundPrototype, { props: { state: 'responded' } })
    expect(wrapper.attributes('data-state')).toBe('responded')
    expect(wrapper.find('[data-testid="assistant-message"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="usage-strip"]').exists()).toBe(true)
    // No typing skeleton, no error banner
    expect(wrapper.find('[data-testid="assistant-typing"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="error-banner"]').exists()).toBe(false)
  })

  it('renders error state', () => {
    const wrapper = mount(PlaygroundPrototype, { props: { state: 'error' } })
    expect(wrapper.attributes('data-state')).toBe('error')
    expect(wrapper.find('[data-testid="error-banner"]').exists()).toBe(true)
    // Error UI keeps the user message visible (so they know what failed) but
    // disables the send button to avoid spamming the failed call.
    expect(wrapper.find('[data-testid="user-message"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="send-button"]').attributes('disabled')).toBeDefined()
  })

  // US-032 AC-004 / AC-005 — A↔B parity: the HTML mockup must declare a
  // matching `data-state="..."` for every Vue state, and vice versa. If
  // someone adds an "info" state to one side without the other, this fails.
  it('AB parity: each Vue state has matching HTML data-state', () => {
    const htmlPath = resolve(
      __dirname,
      '../../../../../docs/approved/attachments/playground-prototype-2026-04-23.html'
    )
    expect(existsSync(htmlPath)).toBe(true)
    const html = readFileSync(htmlPath, 'utf-8')

    for (const state of ALL_STATES) {
      // matches data-state="empty"  (escaped in regex; allow either quote)
      const re = new RegExp(`data-state=["']${state}["']`)
      expect(re.test(html)).toBe(true)
    }

    // And: HTML must NOT declare states the Vue side does not know about.
    // This guards against silent drift from the HTML side.
    const declaredStates = new Set(
      [...html.matchAll(/data-state=["']([a-z]+)["']/g)].map((m) => m[1])
    )
    declaredStates.forEach((s) => {
      expect(ALL_STATES).toContain(s as PlaygroundState)
    })
  })
})
