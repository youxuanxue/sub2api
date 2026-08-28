import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}:${JSON.stringify(params)}` : key
    })
  }
})

import AccountProtocolCapabilities from '../AccountProtocolCapabilities.vue'

describe('AccountProtocolCapabilities', () => {
  it('renders canonical protocol chips in backend order', () => {
    const wrapper = mount(AccountProtocolCapabilities, {
      props: { protocols: ['messages', 'chat_completions', 'responses', 'gemini_generate_content'] }
    })

    expect(wrapper.findAll('[data-protocol]').map((node) => node.attributes('data-protocol'))).toEqual([
      'messages',
      'chat_completions',
      'responses',
      'gemini_generate_content'
    ])
    expect(wrapper.text()).toContain('Messages')
    expect(wrapper.text()).toContain('Chat Completions')
    expect(wrapper.text()).toContain('Responses')
    expect(wrapper.text()).toContain('Gemini Generate Content')
  })

  it('renders a fail-closed empty state without editable controls', () => {
    const wrapper = mount(AccountProtocolCapabilities, { props: { protocols: [] } })

    expect(wrapper.text()).toContain('admin.accounts.protocolCapabilityEmpty')
    expect(wrapper.find('input').exists()).toBe(false)
    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.find('button').exists()).toBe(false)
  })

  it('shows shared endpoint metadata from the capability projection', () => {
    const wrapper = mount(AccountProtocolCapabilities, {
      props: {
        protocols: ['responses'],
        capability: {
          capability_key: 'endpoint-capability-key',
          revision: 7,
          last_probed_at: '2026-08-27T00:00:00Z',
          affected_account_count: 3,
          identity_conflict: false
        }
      }
    })

    expect(wrapper.get('[data-testid="protocol-shared-account-count"]').text()).toContain('3')
    expect(wrapper.get('[data-testid="protocol-last-probed-at"]').attributes('data-last-probed-at')).toBe(
      '2026-08-27T00:00:00Z'
    )
    expect(wrapper.text()).toContain('2026')
  })

  it('presents inconclusive and conflicted facts without claiming an update', () => {
    const wrapper = mount(AccountProtocolCapabilities, {
      props: {
        protocols: [],
        capability: {
          capability_key: 'endpoint-capability-key',
          revision: 8,
          last_probed_at: null,
          affected_account_count: 2,
          identity_conflict: true
        },
        probeOutcome: 'inconclusive'
      }
    })

    expect(wrapper.get('[data-testid="protocol-probe-inconclusive"]').exists()).toBe(true)
    expect(wrapper.get('[data-testid="protocol-capability-conflict"]').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('protocolProbeUpdated')
    expect(wrapper.find('input, select, button').exists()).toBe(false)
  })
})
