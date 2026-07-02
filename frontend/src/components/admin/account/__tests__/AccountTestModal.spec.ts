import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestModal from '../AccountTestModal.vue'

const { getAvailableModels, copyToClipboard } = vi.hoisted(() => ({
  getAvailableModels: vi.fn(),
  copyToClipboard: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getAvailableModels
    }
  }
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  const messages: Record<string, string> = {
    'admin.accounts.imagePromptDefault': 'Generate a cute orange cat astronaut sticker on a clean pastel background.'
  }
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, string | number>) => {
        if (key === 'admin.accounts.imageReceived' && params?.count) {
          return `received-${params.count}`
        }
        return messages[key] || key
      }
    })
  }
})

function createStreamResponse(lines: string[]) {
  const encoder = new TextEncoder()
  const chunks = lines.map((line) => encoder.encode(line))
  let index = 0

  return {
    ok: true,
    body: {
      getReader: () => ({
        read: vi.fn().mockImplementation(async () => {
          if (index < chunks.length) {
            return { done: false, value: chunks[index++] }
          }
          return { done: true, value: undefined }
        })
      })
    }
  } as Response
}

function mountModal(account: Record<string, unknown> = {
  id: 42,
  name: 'Gemini Image Test',
  platform: 'gemini',
  type: 'apikey',
  status: 'active'
}) {
  return mount(AccountTestModal, {
    props: {
      show: false,
      account
    } as any,
    global: {
      stubs: {
        BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
        Select: { template: '<div class="select-stub"></div>' },
        TextArea: {
          props: ['modelValue'],
          emits: ['update:modelValue'],
          template: '<textarea class="textarea-stub" :value="modelValue" @input="$emit(\'update:modelValue\', $event.target.value)" />'
        },
        Icon: true
      }
    }
  })
}

describe('AccountTestModal', () => {
  beforeEach(() => {
    getAvailableModels.mockResolvedValue([
      { id: 'gemini-2.0-flash', display_name: 'Gemini 2.0 Flash' },
      { id: 'gemini-2.5-flash-image', display_name: 'Gemini 2.5 Flash Image' },
      { id: 'gemini-3.1-flash-image', display_name: 'Gemini 3.1 Flash Image' }
    ])
    copyToClipboard.mockReset()
    Object.defineProperty(globalThis, 'localStorage', {
      value: {
        getItem: vi.fn((key: string) => (key === 'auth_token' ? 'test-token' : null)),
        setItem: vi.fn(),
        removeItem: vi.fn(),
        clear: vi.fn()
      },
      configurable: true
    })
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"gemini-2.5-flash-image"}\n',
        'data: {"type":"image","image_url":"data:image/png;base64,QUJD","mime_type":"image/png"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any
  })

  afterEach(() => {
    vi.restoreAllMocks()
  })

  it('gemini 图片模型测试会携带提示词并渲染图片预览', async () => {
    const wrapper = mountModal()
    await wrapper.setProps({ show: true })
    await flushPromises()

    const promptInput = wrapper.find('textarea.textarea-stub')
    expect(promptInput.exists()).toBe(true)
    await promptInput.setValue('draw a tiny orange cat astronaut')

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'gemini-3.1-flash-image',
      prompt: 'draw a tiny orange cat astronaut'
    })

    const preview = wrapper.find('img[alt="test-image-1"]')
    expect(preview.exists()).toBe(true)
    expect(preview.attributes('src')).toBe('data:image/png;base64,QUJD')
  })

  // An empty model dropdown for a normal account (e.g. an anthropic OAuth
  // account, which always returns the Claude default catalog when reachable)
  // means the getAvailableModels request FAILED — the modal must surface why
  // (404 unavailable / 401 session / generic) instead of a misleading empty
  // "no options" picker. (2026-06-22 edge-us6 oh-3-e incident.)
  function mountWith(account: Record<string, unknown>) {
    return mount(AccountTestModal, {
      props: { show: false, account } as any,
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Select: { template: '<div class="select-stub"></div>' },
          TextArea: true,
          Icon: true
        }
      }
    })
  }

  it('surfaces a 404 as "account unavailable" instead of an empty dropdown, and retry reloads', async () => {
    getAvailableModels.mockRejectedValueOnce({ response: { status: 404 } })
    const wrapper = mountWith({ id: 11, name: 'oh-3-e', platform: 'anthropic', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()

    // the broken empty picker is replaced by a clear error…
    expect(wrapper.find('.select-stub').exists()).toBe(false)
    expect(wrapper.text()).toContain('admin.accounts.loadModelsUnavailable')

    // …and retry re-loads (now succeeding) → the picker comes back, error clears.
    getAvailableModels.mockResolvedValueOnce([{ id: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' }])
    const retry = wrapper.findAll('button').find((b) => b.text().includes('admin.accounts.retry'))
    expect(retry).toBeTruthy()
    await retry!.trigger('click')
    await flushPromises()
    expect(wrapper.find('.select-stub').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.loadModelsUnavailable')
  })

  it('surfaces a 401 as a session-expired error', async () => {
    getAvailableModels.mockRejectedValueOnce({ response: { status: 401 } })
    const wrapper = mountWith({ id: 11, name: 'oh-3-e', platform: 'anthropic', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.loadModelsAuthExpired')
    expect(wrapper.find('.select-stub').exists()).toBe(false)
  })

  it('surfaces a generic load failure', async () => {
    getAvailableModels.mockRejectedValueOnce(new Error('network'))
    const wrapper = mountWith({ id: 11, name: 'oh-3-e', platform: 'anthropic', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.text()).toContain('admin.accounts.loadModelsFailed')
    expect(wrapper.find('.select-stub').exists()).toBe(false)
  })

  it('shows the picker normally when models load (no error)', async () => {
    getAvailableModels.mockResolvedValueOnce([{ id: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' }])
    const wrapper = mountWith({ id: 11, name: 'oh-3-e', platform: 'anthropic', type: 'oauth', status: 'active' })
    await wrapper.setProps({ show: true })
    await flushPromises()
    expect(wrapper.find('.select-stub').exists()).toBe(true)
    expect(wrapper.text()).not.toContain('admin.accounts.loadModelsFailed')
  })

  it('prefers short Kiro model IDs for prod mirror stubs', async () => {
    getAvailableModels.mockResolvedValueOnce([
      { id: 'claude-sonnet-4-5-20250929', display_name: 'Claude Sonnet 4.5 dated' },
      { id: 'claude-sonnet-4-5', display_name: 'Claude Sonnet 4.5' }
    ])
    const wrapper = mountWith({
      id: 12,
      name: 'kiro-us5',
      platform: 'anthropic',
      type: 'apikey',
      status: 'active',
      credentials: { mirror_platform: 'kiro' }
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const startButton = wrapper.findAll('button').find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()
    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({
      model_id: 'claude-sonnet-4-5'
    })
  })

  it('defaults antigravity account test to first gemini model from admin catalog', async () => {
    getAvailableModels.mockResolvedValueOnce([
      { id: 'gemini-3-flash', display_name: 'Gemini 3 Flash' },
      { id: 'gemini-pro-agent', display_name: 'Gemini 3.1 Pro (High)' }
    ])
    const wrapper = mountWith({
      id: 701,
      name: 'antigravity-or1-ls-b',
      platform: 'antigravity',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const startButton = wrapper.findAll('button').find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()
    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toMatchObject({
      model_id: 'gemini-3-flash'
    })
  })

  it('grok 账号测试默认选择 Grok 模型', async () => {
    getAvailableModels.mockResolvedValue([
      { id: 'grok-4.3', display_name: 'Grok 4.3' },
      { id: 'grok-build-0.1', display_name: 'Grok Build 0.1' }
    ])
    global.fetch = vi.fn().mockResolvedValue(
      createStreamResponse([
        'data: {"type":"test_start","model":"grok-4.3"}\n',
        'data: {"type":"content","text":"ok"}\n',
        'data: {"type":"test_complete","success":true}\n'
      ])
    ) as any

    const wrapper = mountModal({
      id: 13,
      name: 'Grok Account',
      platform: 'grok',
      type: 'oauth',
      status: 'active'
    })
    await wrapper.setProps({ show: true })
    await flushPromises()

    const buttons = wrapper.findAll('button')
    const startButton = buttons.find((button) => button.text().includes('admin.accounts.startTest'))
    expect(startButton).toBeTruthy()

    await startButton!.trigger('click')
    await flushPromises()

    expect(global.fetch).toHaveBeenCalledTimes(1)
    const [, request] = (global.fetch as any).mock.calls[0]
    expect(JSON.parse(request.body)).toEqual({
      model_id: 'grok-4.3',
      prompt: ''
    })
  })

  // Regression (#900 lazyMount): AccountsView lazy-mounts this modal, so on first
  // open it is CREATED with show already true. A non-immediate show-watch never
  // fires for that mount → models never loaded → empty picker. onMounted must load
  // when mounted already-shown. Note the props start with show:true (not toggled).
  it('loads models on first lazy-mount (created already shown), not only on reopen', async () => {
    getAvailableModels.mockReset()
    getAvailableModels.mockResolvedValueOnce([{ id: 'claude-sonnet-4-6', display_name: 'Claude Sonnet 4.6' }])
    const wrapper = mount(AccountTestModal, {
      props: { show: true, account: { id: 2, name: 'tokenkey-edge-us-or1-ls-b', platform: 'anthropic', type: 'oauth', status: 'active' } } as any,
      global: {
        stubs: {
          BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
          Select: { template: '<div class="select-stub"></div>' },
          TextArea: true,
          Icon: true
        }
      }
    })
    await flushPromises()
    expect(getAvailableModels).toHaveBeenCalledWith(2)
    expect(wrapper.find('.select-stub').exists()).toBe(true)
  })
})
