// Composable-level tests for the NewAPI (5th platform) modal logic.
// Covers the corner cases that are hard to surface from full-modal mounting:
//   - populateFromAccount() correctly classifies whitelist vs mapping mode
//     from credentials.model_mapping shapes.
//   - populateFromAccount() resets fetchLoading so a stale in-flight from
//     a previously edited account does not freeze the new account's
//     «获取模型列表» button (UX bug guard).
//   - buildSubmitBundle() honors create vs edit semantics for api_key:
//     create → empty key rejected; edit → empty key accepted (caller will
//     fall back to currentCredentials.api_key).
//   - buildSubmitBundle() emits whitelist-mode model_mapping as
//     {model:model} mirror, mapping-mode as {from:to}.
//   - fetchModelsDisabled honors stored-credential fallback in edit mode.

import { describe, expect, it, vi, beforeEach } from 'vitest'
import { nextTick } from 'vue'

const {
  showErrorMock,
  showSuccessMock,
  showInfoMock,
  fetchUpstreamModelsMock,
  listChannelTypesMock,
} = vi.hoisted(() => ({
  showErrorMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showInfoMock: vi.fn(),
  fetchUpstreamModelsMock: vi.fn(),
  listChannelTypesMock: vi.fn(),
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: showErrorMock,
    showSuccess: showSuccessMock,
    showInfo: showInfoMock,
  }),
}))

vi.mock('@/api/admin/channels', () => ({
  fetchUpstreamModels: fetchUpstreamModelsMock,
  listChannelTypes: listChannelTypesMock,
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    channels: {
      listChannelTypes: listChannelTypesMock,
    },
  },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string, params?: Record<string, unknown>) =>
        params ? `${key}::${JSON.stringify(params)}` : key,
    }),
  }
})

import { useTkAccountNewApiPlatform } from '../useTkAccountNewApiPlatform'

describe('useTkAccountNewApiPlatform', () => {
  beforeEach(() => {
    showErrorMock.mockReset()
    showSuccessMock.mockReset()
    showInfoMock.mockReset()
    fetchUpstreamModelsMock.mockReset()
    listChannelTypesMock.mockReset()
    listChannelTypesMock.mockResolvedValue([])
  })

  describe('populateFromAccount', () => {
    it('whitelist mode 推断：所有 from===to 的 model_mapping → whitelist 模式 + allowedModels 填充', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.populateFromAccount({
        channel_type: 14,
        credentials: {
          base_url: 'https://api.deepseek.com',
          model_mapping: { 'deepseek-chat': 'deepseek-chat', 'deepseek-coder': 'deepseek-coder' },
        },
      })
      expect(hook.channelType.value).toBe(14)
      expect(hook.baseUrl.value).toBe('https://api.deepseek.com')
      expect(hook.apiKey.value).toBe('')
      expect(hook.restrictionMode.value).toBe('whitelist')
      expect(hook.allowedModels.value).toEqual(['deepseek-chat', 'deepseek-coder'])
      expect(hook.modelMappings.value).toEqual([])
    })

    it('mapping mode 推断：含 from!==to 的条目 → mapping 模式 + modelMappings 填充', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.populateFromAccount({
        channel_type: 1,
        credentials: {
          base_url: 'https://api.openai.com',
          model_mapping: { 'gpt-4': 'gpt-4-turbo-2024-04-09', 'gpt-3.5': 'gpt-3.5' },
        },
      })
      expect(hook.restrictionMode.value).toBe('mapping')
      expect(hook.allowedModels.value).toEqual([])
      expect(hook.modelMappings.value).toEqual([
        { from: 'gpt-4', to: 'gpt-4-turbo-2024-04-09' },
        { from: 'gpt-3.5', to: 'gpt-3.5' },
      ])
    })

    it('空 model_mapping → 默认 whitelist 模式 + 空选择（语义：允许所有）', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.populateFromAccount({ channel_type: 14, credentials: { base_url: 'x' } })
      expect(hook.restrictionMode.value).toBe('whitelist')
      expect(hook.allowedModels.value).toEqual([])
      expect(hook.modelMappings.value).toEqual([])
    })

    it('清空 fetchLoading：上一账号 in-flight 不污染新账号的「获取模型列表」按钮（UX bug guard）', async () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      // 模拟前一个账号的 fetch 还在 loading
      let _resolve: (v: string[]) => void = () => {}
      fetchUpstreamModelsMock.mockReturnValue(new Promise((r) => { _resolve = r }))
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      void hook.handleFetchUpstreamModels()
      await nextTick()
      expect(hook.fetchModelsLoading.value).toBe(true)
      // 切换到新账号（同一 modal 实例）
      hook.populateFromAccount({ channel_type: 1, credentials: {} })
      expect(hook.fetchModelsLoading.value).toBe(false)
    })

    it('status_code_mapping 支持字符串 / 对象两种持久化形态', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.populateFromAccount({
        channel_type: 1,
        credentials: { status_code_mapping: '{"404":"500"}' },
      })
      expect(hook.statusCodeMapping.value).toBe('{"404":"500"}')
      hook.populateFromAccount({
        channel_type: 1,
        credentials: { status_code_mapping: { '404': '500' } as unknown as string },
      })
      expect(hook.statusCodeMapping.value).toBe('{"404":"500"}')
    })
  })

  describe('buildSubmitBundle', () => {
    it("create 模式：channel_type=0 拒绝", () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).toBeNull()
      expect(showErrorMock).toHaveBeenCalledWith(
        'admin.accounts.newApiPlatform.pleaseSelectChannelType'
      )
    })

    it('create 模式：base_url 留空且 catalog 也无默认值 → 拒绝', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 9999
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).toBeNull()
      expect(showErrorMock).toHaveBeenCalledWith(
        'admin.accounts.newApiPlatform.pleaseEnterBaseUrl'
      )
    })

    it('create 模式：api_key 必填', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).toBeNull()
      expect(showErrorMock).toHaveBeenCalledWith(
        'admin.accounts.newApiPlatform.pleaseEnterApiKey'
      )
    })

    it('edit 模式：api_key 留空 → bundle.credentials 不含 api_key 键（让父组件用 stored credential 兜底）', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      const bundle = hook.buildSubmitBundle('edit')
      expect(bundle).not.toBeNull()
      expect(bundle!.credentials).not.toHaveProperty('api_key')
      expect(bundle!.credentials.base_url).toBe('https://api.deepseek.com')
      expect(bundle!.channelType).toBe(14)
    })

    it('whitelist 模式：构造 {model: model} 镜像', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      hook.restrictionMode.value = 'whitelist'
      hook.allowedModels.value = ['deepseek-chat', 'deepseek-coder']
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).not.toBeNull()
      expect(bundle!.credentials.model_mapping).toEqual({
        'deepseek-chat': 'deepseek-chat',
        'deepseek-coder': 'deepseek-coder',
      })
    })

    it('mapping 模式：构造 {from: to} 重写', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 1
      hook.baseUrl.value = 'https://api.openai.com'
      hook.apiKey.value = 'sk-test'
      hook.restrictionMode.value = 'mapping'
      hook.modelMappings.value = [{ from: 'gpt-4', to: 'gpt-4-turbo' }]
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).not.toBeNull()
      expect(bundle!.credentials.model_mapping).toEqual({ 'gpt-4': 'gpt-4-turbo' })
    })

    it('两种模式都为空 → bundle.credentials 不含 model_mapping 键（语义：允许所有模型）', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).not.toBeNull()
      expect(bundle!.credentials).not.toHaveProperty('model_mapping')
    })

    it('status_code_mapping 不是合法 JSON 对象 → 拒绝并 showError', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      hook.statusCodeMapping.value = '[1,2,3]'
      const bundle = hook.buildSubmitBundle('create')
      expect(bundle).toBeNull()
      expect(showErrorMock).toHaveBeenCalledWith(
        'admin.accounts.newApiPlatform.jsonObjectRequired'
      )
    })
  })

  describe('fetchModelsDisabled', () => {
    it('Create modal（无 storedAccount）：base_url + api_key 都填才启用', () => {
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      // 都没填 → disabled
      expect(hook.fetchModelsDisabled.value).toBe(true)
      hook.baseUrl.value = 'x'
      expect(hook.fetchModelsDisabled.value).toBe(true) // 缺 key
      hook.apiKey.value = 'sk-test'
      expect(hook.fetchModelsDisabled.value).toBe(false)
    })

    it('Edit modal（有 storedAccount + 同 channel_type）：api_key 留空也启用（走 stored credential）', () => {
      const hook = useTkAccountNewApiPlatform({
        isNewapi: () => true,
        storedAccount: () => ({ id: 42, channel_type: 14 }),
      })
      hook.channelType.value = 14
      hook.baseUrl.value = 'x'
      // api_key 留空仍可点
      expect(hook.fetchModelsDisabled.value).toBe(false)
    })

    it('Edit modal：用户改了 channel_type 与持久化值不一致 → 不能再用 stored credential（disabled）', () => {
      const hook = useTkAccountNewApiPlatform({
        isNewapi: () => true,
        storedAccount: () => ({ id: 42, channel_type: 14 }),
      })
      hook.channelType.value = 99 // 用户改成了不同的 channel_type
      hook.baseUrl.value = 'x'
      // stored.channel_type !== channelType → 必须重输 api_key
      expect(hook.fetchModelsDisabled.value).toBe(true)
    })
  })

  describe('handleFetchUpstreamModels', () => {
    it('成功 → 写入 allowedModels + 强制切回 whitelist 模式 + showSuccess', async () => {
      fetchUpstreamModelsMock.mockResolvedValue(['m1', 'm2', 'm3'])
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      hook.restrictionMode.value = 'mapping' // 起点是 mapping
      await hook.handleFetchUpstreamModels()
      expect(hook.allowedModels.value).toEqual(['m1', 'm2', 'm3'])
      expect(hook.restrictionMode.value).toBe('whitelist') // 强制切回
      expect(showSuccessMock).toHaveBeenCalledWith(
        expect.stringContaining('admin.accounts.newApiPlatform.fetchUpstreamModelsSuccess')
      )
    })

    it('上游返回空 → showInfo 且不污染 allowedModels', async () => {
      fetchUpstreamModelsMock.mockResolvedValue([])
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      hook.allowedModels.value = ['existing']
      await hook.handleFetchUpstreamModels()
      expect(hook.allowedModels.value).toEqual(['existing']) // 未被覆盖
      expect(showInfoMock).toHaveBeenCalledWith(
        'admin.accounts.newApiPlatform.fetchUpstreamModelsEmpty'
      )
    })

    it('请求失败 → showError 并把 fetchLoading 复位', async () => {
      fetchUpstreamModelsMock.mockRejectedValue(new Error('upstream 502'))
      const hook = useTkAccountNewApiPlatform({ isNewapi: () => true })
      hook.channelType.value = 14
      hook.baseUrl.value = 'https://api.deepseek.com'
      hook.apiKey.value = 'sk-test'
      await hook.handleFetchUpstreamModels()
      expect(showErrorMock).toHaveBeenCalled()
      expect(hook.fetchModelsLoading.value).toBe(false)
    })
  })
})
