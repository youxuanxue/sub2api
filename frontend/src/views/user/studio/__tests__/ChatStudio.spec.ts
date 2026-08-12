import { mount, flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createI18n } from 'vue-i18n'
import { GatewayRequestTimeoutError } from '@/api/playground'
import ChatStudio from '../ChatStudio.vue'

const { gatewayChatCompletion } = vi.hoisted(() => ({
  gatewayChatCompletion: vi.fn(),
}))

vi.mock('@/api/playground', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/playground')>()
  return {
    ...actual,
    gatewayChatCompletion,
  }
})

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() }),
}))

function mountChatStudio(): ReturnType<typeof mount> {
  const i18n = createI18n({
    legacy: false,
    locale: 'en',
    messages: {
      en: {
        studio: {
          chat: {
            model: 'Model',
            temperature: 'Temperature',
            maxTokens: 'Max tokens',
            emptyHint: 'Empty',
            inputPlaceholder: 'Type…',
            send: 'Send',
            sending: 'Sending…',
            cancel: 'Cancel',
            clear: 'Clear chat',
            systemPrompt: 'System prompt',
            limitsHint: 'Limits {turns} {maxTok}',
            integrationsTitle: 'Apps',
            integrationsHint: 'Hint',
            integrationsAppHint: 'App',
            integrationsManualKeyHint: 'Manual',
            integrationsManualKeyShort: 'paste',
            copyBaseUrl: 'Copy base',
            copyKey: 'Copy key',
            roleUser: 'You',
            roleAssistant: 'Assistant',
            avatarUser: 'Me',
            avatarAssistant: 'AI',
            lastUsage: 'Usage',
            promptTokens: 'Prompt',
            completionTokens: 'Completion',
            totalTokens: 'Total',
            truncatedHint:
              'The reply was cut off because it reached the Max tokens limit. Increase Max tokens (up to {maxTok}) and send again.',
            timedOut: 'Timed out after {timeoutSec}s',
            cancelled: 'Cancelled',
            requestFailed: 'Failed',
          },
        },
      },
    },
  })
  return mount(ChatStudio, {
    props: {
      apiKey: 'sk-test',
      gatewayBase: 'https://api.example',
      availableIds: new Set(['claude-fable-5']),
    },
    global: { plugins: [i18n] },
  })
}

describe('ChatStudio', () => {
  beforeEach(() => {
    gatewayChatCompletion.mockReset()
  })

  it('shows a truncation hint when finish_reason is length', async () => {
    gatewayChatCompletion.mockResolvedValue({
      choices: [{ finish_reason: 'length', message: { content: 'partial table | col |' } }],
      usage: { prompt_tokens: 100, completion_tokens: 1024, total_tokens: 1124 },
    })

    const wrapper = mountChatStudio()
    await wrapper.get('textarea').setValue('Write a long survey guide')
    await wrapper.get('[data-testid="studio-chat-send"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="studio-chat-truncated"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('partial table')
  })

  it('does not show truncation hint for a normal stop', async () => {
    gatewayChatCompletion.mockResolvedValue({
      choices: [{ finish_reason: 'stop', message: { content: 'complete answer' } }],
      usage: { prompt_tokens: 10, completion_tokens: 5, total_tokens: 15 },
    })

    const wrapper = mountChatStudio()
    await wrapper.get('textarea').setValue('Say hi')
    await wrapper.get('[data-testid="studio-chat-send"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="studio-chat-truncated"]').exists()).toBe(false)
    expect(wrapper.text()).toContain('complete answer')
  })

  it('restores draft and drops the optimistic user turn on timeout', async () => {
    gatewayChatCompletion.mockRejectedValue(new GatewayRequestTimeoutError(180_000))

    const wrapper = mountChatStudio()
    await wrapper.get('textarea').setValue('Long prompt')
    await wrapper.get('[data-testid="studio-chat-send"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="studio-chat-error"]').exists()).toBe(true)
    expect((wrapper.get('textarea').element as HTMLTextAreaElement).value).toBe('Long prompt')
    expect(wrapper.text()).not.toContain('Long prompt')
  })
})
