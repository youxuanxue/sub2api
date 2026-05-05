/**
 * US-032 P1-B prototype A 件 — PlaygroundPrototype.vue tests.
 *
 * Spec: docs/approved/user-cold-start.md §11
 *       .testing/user-stories/stories/US-032-playground-prototype-AB.md
 */

import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'
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
})
