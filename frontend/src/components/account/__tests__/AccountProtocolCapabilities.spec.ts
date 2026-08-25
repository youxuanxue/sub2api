import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountProtocolCapabilities from '../AccountProtocolCapabilities.vue'

describe('AccountProtocolCapabilities', () => {
  it('renders canonical protocol chips in backend order', () => {
    const wrapper = mount(AccountProtocolCapabilities, {
      props: { protocols: ['messages', 'chat_completions', 'responses'] }
    })

    expect(wrapper.findAll('[data-protocol]').map((node) => node.attributes('data-protocol'))).toEqual([
      'messages',
      'chat_completions',
      'responses'
    ])
    expect(wrapper.text()).toContain('Messages')
    expect(wrapper.text()).toContain('Chat Completions')
    expect(wrapper.text()).toContain('Responses')
  })

  it('renders a fail-closed empty state without editable controls', () => {
    const wrapper = mount(AccountProtocolCapabilities, { props: { protocols: [] } })

    expect(wrapper.text()).toContain('未检测到可用文本协议')
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })
})
