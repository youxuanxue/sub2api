import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { nextTick } from 'vue'

import SupplierSourcesView from '../SupplierSourcesView.vue'

const { list, create, update, priorityPreview, discoverModels, getDiscoverModelsJob, sync, routeQuery, channelTypes } = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  update: vi.fn(),
  priorityPreview: vi.fn(),
  discoverModels: vi.fn(),
  getDiscoverModelsJob: vi.fn(),
  sync: vi.fn(),
  routeQuery: {} as Record<string, unknown>,
  channelTypes: { value: [
    { channel_type: 1, name: 'OpenAI', base_url: 'https://api.openai.com/v1' },
    { channel_type: 17, name: 'Ali', base_url: 'https://dashscope.aliyuncs.com/compatible-mode/v1' },
    { channel_type: 46, name: 'Baidu V2', base_url: 'https://qianfan.baidubce.com' },
  ] },
}))

vi.mock('@/composables/useNewApiChannelTypes', () => ({
  useNewApiChannelTypes: () => ({
    types: channelTypes,
    loading: false,
    error: null,
    load: vi.fn().mockResolvedValue(undefined),
  }),
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { supplierSources: { list, create, update, priorityPreview, discoverModels, getDiscoverModelsJob, sync } },
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

vi.mock('vue-router', () => ({
  useRoute: () => ({ query: routeQuery }),
}))

const source = {
  id: 7,
  supplier_name: '佳杰',
  channel_name: 'stbl-5',
  channel_type: 1,
  endpoint: 'https://token.vstecscloud.com/v1',
  base_priority: 100,
  account_concurrency: 1000,
  notes: '首批最低折扣',
  models: [{
    client_model_id: 'deepseek-v4-pro',
    upstream_model_id: 'deepseek-v4-pro',
    purchase_ratio: 0.5,
  }],
  created_at: '2026-08-28T01:00:00Z',
  updated_at: '2026-08-28T01:00:00Z',
}

describe('SupplierSourcesView', () => {
  beforeEach(() => {
    for (const key of Object.keys(routeQuery)) delete routeQuery[key]
    list.mockReset().mockResolvedValue([])
    create.mockReset()
    update.mockReset()
    priorityPreview.mockReset().mockResolvedValue({ entries: [], warnings: [] })
    discoverModels.mockReset().mockResolvedValue({
      source_id: 7,
      probe_status: 'completed',
      probe_total: 0,
      probe_done: 0,
      upstream_models: [],
      normalized_models: source.models,
      normalized_changes: [],
      suggested_appends: [],
      rejected_candidates: [],
      configured_issues: [],
      probe_results: [],
      needs_confirmation: false,
    })
    getDiscoverModelsJob.mockReset()
    sync.mockReset()
  })

  it('defaults a new source to channel default and base priority 100', async () => {
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    expect((wrapper.get('[data-test="channel-name"]').element as HTMLInputElement).value).toBe('default')
    expect((wrapper.get('[data-test="base-priority"]').element as HTMLInputElement).value).toBe('100')
  })

  it('offers and hydrates BaiduV2 while preserving a custom endpoint during source selection', async () => {
    const qianfanSource = {
      ...source,
      channel_type: 46,
      endpoint: 'https://qianfan.baidubce.com',
    }
    const aliSource = {
      ...source,
      id: 8,
      supplier_name: 'Ali',
      channel_type: 17,
      endpoint: 'https://dashscope-proxy.example.com',
    }
    list.mockResolvedValueOnce([qianfanSource, aliSource])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    const channelType = wrapper.get('[data-test="channel-type"]')
    expect(channelType.findAll('option').map(option => option.attributes('value'))).toContain('46')

    await channelType.setValue('46')
    await nextTick()
    expect((wrapper.get('[data-test="endpoint"]').element as HTMLInputElement).value).toBe(
      'https://qianfan.baidubce.com',
    )

    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await nextTick()
    expect((channelType.element as HTMLSelectElement).value).toBe('46')

    await wrapper.get('[data-test="source-select-8"]').trigger('click')
    await nextTick()
    expect((channelType.element as HTMLSelectElement).value).toBe('17')
    expect((wrapper.get('[data-test="endpoint"]').element as HTMLInputElement).value).toBe(
      aliSource.endpoint,
    )
    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="sync-save-first"]').exists()).toBe(false)
  })

  it('shows blank purchase ratio as band 6 and priority base plus 6', async () => {
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    expect(wrapper.get('[data-test="model-band-0"]').text()).toContain('6')
    expect(wrapper.get('[data-test="model-priority-0"]').text()).toContain('106')
  })

  it('selects the supplier source requested by source_id after loading the list', async () => {
    routeQuery.source_id = '7'
    list.mockResolvedValueOnce([
      { ...source, id: 8, supplier_name: 'FMGo', channel_name: 'seedance' },
      source,
    ])

    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    expect((wrapper.get('[data-test="supplier-name"]').element as HTMLInputElement).value).toBe('佳杰')
    expect(wrapper.get('[data-test="source-select-7"]').classes()).toContain('border-primary-500')
  })

  it('saves new and existing sources through create or update only', async () => {
    create.mockResolvedValueOnce(source)
    list.mockResolvedValueOnce([source])
    update.mockResolvedValueOnce({ ...source, notes: '合同已复核' })
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    await wrapper.get('[data-test="new-source"]').trigger('click')
    await wrapper.get('[data-test="supplier-name"]').setValue('佳杰')
    await wrapper.get('[data-test="channel-name"]').setValue('stbl-5')
    await wrapper.get('[data-test="endpoint"]').setValue('https://token.vstecscloud.com/v1')
    await wrapper.get('[data-test="credential"]').setValue('secret')
    await wrapper.get('[data-test="save-source"]').trigger('submit')
    await flushPromises()

    expect(create).toHaveBeenCalledWith(expect.objectContaining({ base_priority: 100, models: [] }))
    expect(priorityPreview).not.toHaveBeenCalled()
    expect(sync).not.toHaveBeenCalled()

    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="notes"]').setValue('合同已复核')
    await wrapper.get('[data-test="save-source"]').trigger('submit')
    await flushPromises()

    expect(update).toHaveBeenCalledWith(7, expect.objectContaining({ credential: '', notes: '合同已复核' }))
  })

  it('serializes save and sync submissions for the selected source', async () => {
    list.mockResolvedValueOnce([source])
    let resolveUpdate!: (value: typeof source) => void
    update.mockReturnValueOnce(new Promise(resolve => { resolveUpdate = resolve }))
    let resolveSync!: (value: {
      source_id: number
      probe_results: never[]
      changes: never[]
    }) => void
    sync.mockReturnValueOnce(new Promise(resolve => { resolveSync = resolve }))
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')

    await wrapper.get('[data-test="save-source"]').trigger('submit')
    await nextTick()

    expect(wrapper.get('[data-test="save-source"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeDefined()

    resolveUpdate(source)
    await flushPromises()
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await nextTick()

    expect(wrapper.get('[data-test="save-source"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeDefined()
    expect(discoverModels).toHaveBeenCalledWith(7)

    resolveSync({ source_id: 7, probe_results: [], changes: [] })
    await flushPromises()
  })

  it('requires saving edited supplier facts before syncing the selected source', async () => {
    list.mockResolvedValueOnce([source])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')

    await wrapper.get('[data-test="notes"]').setValue('尚未保存的修改')
    await nextTick()

    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeDefined()
    expect(wrapper.get('[data-test="sync-save-first"]').text()).toContain(
      'admin.supplierSources.saveBeforeSync',
    )
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    expect(discoverModels).not.toHaveBeenCalled()
    expect(sync).not.toHaveBeenCalled()

    await wrapper.get('[data-test="notes"]').setValue(source.notes)
    await nextTick()

    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeUndefined()
    expect(wrapper.find('[data-test="sync-save-first"]').exists()).toBe(false)
  })

  it('applies discover normalize to the form and keeps suggestions opt-in', async () => {
    list.mockResolvedValueOnce([source])
    discoverModels.mockResolvedValueOnce({
      source_id: 7,
      probe_status: 'completed',
      probe_total: 1,
      probe_done: 1,
      upstream_models: [{ id: 'deepseek-v4-pro', type: 'chat' }, { id: 'glm-5.1', type: 'chat' }],
      normalized_models: [{
        client_model_id: 'deepseek-v4-pro',
        upstream_model_id: 'deepseek-v4-pro',
        purchase_ratio: 0.5,
      }],
      normalized_changes: [{
        from_client_model_id: 'DeepSeek-V4-Pro',
        from_upstream_model_id: 'DeepSeek-V4-Pro',
        to_client_model_id: 'deepseek-v4-pro',
        to_upstream_model_id: 'deepseek-v4-pro',
      }],
      suggested_appends: [{
        client_model_id: 'glm-5.1',
        upstream_model_id: 'glm-5.1',
        purchase_ratio: 1,
      }],
      rejected_candidates: [{
        upstream_model_id: 'embedding-v1',
        type: 'embeddings',
        reason: 'non_chat_type',
      }],
      configured_issues: [],
      probe_results: [{
        client_model_id: 'glm-5.1',
        upstream_model_id: 'glm-5.1',
        status: 'passed',
      }],
      needs_confirmation: true,
    })
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    expect(sync).not.toHaveBeenCalled()
    expect(wrapper.get('[data-test="discover-needs-save"]').text()).toContain(
      'admin.supplierSources.discoverNeedsSave',
    )
    expect(wrapper.get('[data-test="discover-result"]').text()).toContain('glm-5.1')
    expect(wrapper.findAll('[data-test="upstream-model-id"]')).toHaveLength(1)
    expect((wrapper.get('[data-test="upstream-model-id"]').element as HTMLInputElement).value)
      .toBe('deepseek-v4-pro')

    await wrapper.get('[data-test="append-suggested"]').trigger('click')
    await nextTick()
    const upstreamInputs = wrapper.findAll('[data-test="upstream-model-id"]')
    expect(upstreamInputs).toHaveLength(2)
    expect((upstreamInputs[1].element as HTMLInputElement).value).toBe('glm-5.1')
    expect(wrapper.get('[data-test="sync-source"]').attributes('disabled')).toBeDefined()
  })

  it('continues to account sync when discover only has optional suggestions', async () => {
    list.mockResolvedValueOnce([source])
    discoverModels.mockResolvedValueOnce({
      source_id: 7,
      probe_status: 'completed',
      probe_total: 1,
      probe_done: 1,
      upstream_models: [{ id: 'deepseek-v4-pro', type: 'chat' }, { id: 'glm-5.1', type: 'chat' }],
      normalized_models: source.models,
      normalized_changes: [],
      suggested_appends: [{
        client_model_id: 'glm-5.1',
        upstream_model_id: 'glm-5.1',
        purchase_ratio: 1,
      }],
      rejected_candidates: [],
      configured_issues: [],
      probe_results: [],
      needs_confirmation: false,
    })
    sync.mockResolvedValueOnce({ source_id: 7, probe_results: [], changes: [] })
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    expect(sync).toHaveBeenCalledWith(7)
    expect(wrapper.get('[data-test="discover-result"]').text()).toContain('glm-5.1')
    expect(wrapper.find('[data-test="discover-needs-save"]').exists()).toBe(false)
  })

  it('polls a running models-discover job until completed before syncing', async () => {
    vi.useFakeTimers()
    list.mockResolvedValueOnce([source])
    discoverModels.mockResolvedValueOnce({
      source_id: 7,
      job_id: 'job-async-1',
      probe_status: 'running',
      probe_total: 2,
      probe_done: 0,
      upstream_models: [{ id: 'deepseek-v4-pro', type: 'chat' }, { id: 'glm-5.1', type: 'chat' }],
      normalized_models: source.models,
      normalized_changes: [],
      suggested_appends: [],
      rejected_candidates: [],
      configured_issues: [],
      probe_results: [],
      needs_confirmation: false,
    })
    getDiscoverModelsJob
      .mockResolvedValueOnce({
        source_id: 7,
        job_id: 'job-async-1',
        probe_status: 'running',
        probe_total: 2,
        probe_done: 1,
        upstream_models: [{ id: 'deepseek-v4-pro', type: 'chat' }, { id: 'glm-5.1', type: 'chat' }],
        normalized_models: source.models,
        normalized_changes: [],
        suggested_appends: [{
          client_model_id: 'glm-5.1',
          upstream_model_id: 'glm-5.1',
          purchase_ratio: 1,
        }],
        rejected_candidates: [],
        configured_issues: [],
        probe_results: [],
        needs_confirmation: false,
      })
      .mockResolvedValueOnce({
        source_id: 7,
        job_id: 'job-async-1',
        probe_status: 'completed',
        probe_total: 2,
        probe_done: 2,
        upstream_models: [{ id: 'deepseek-v4-pro', type: 'chat' }, { id: 'glm-5.1', type: 'chat' }],
        normalized_models: source.models,
        normalized_changes: [],
        suggested_appends: [{
          client_model_id: 'glm-5.1',
          upstream_model_id: 'glm-5.1',
          purchase_ratio: 1,
        }],
        rejected_candidates: [],
        configured_issues: [],
        probe_results: [],
        needs_confirmation: false,
      })
    sync.mockResolvedValueOnce({ source_id: 7, probe_results: [], changes: [] })

    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="discover-probe-progress"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="append-suggested"]').exists()).toBe(false)
    expect(sync).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(getDiscoverModelsJob).toHaveBeenCalledWith(7, 'job-async-1')
    expect(wrapper.find('[data-test="append-suggested"]').exists()).toBe(false)

    await vi.advanceTimersByTimeAsync(1000)
    await flushPromises()
    expect(sync).toHaveBeenCalledWith(7)
    expect(wrapper.find('[data-test="discover-probe-progress"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="append-suggested"]').exists()).toBe(true)

    vi.useRealTimers()
  })

  it('shows discover failure message and failed_step outside the sync-result block', async () => {
    list.mockResolvedValueOnce([source])
    discoverModels.mockRejectedValueOnce(Object.assign(
      new Error('Supplier model list request failed with HTTP 401'),
      {
        status: 502,
        data: {
          source_id: 7,
          probe_status: 'failed',
          probe_total: 0,
          probe_done: 0,
          upstream_models: [],
          normalized_models: [],
          normalized_changes: [],
          suggested_appends: [],
          rejected_candidates: [],
          configured_issues: [],
          probe_results: [],
          needs_confirmation: false,
          failed_step: 'list_upstream_models',
        },
      },
    ))
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    expect(wrapper.get('[data-test="sync-error"]').text()).toContain(
      'Supplier model list request failed with HTTP 401',
    )
    expect(wrapper.get('[data-test="discover-failed-step"]').text()).toContain('list_upstream_models')
    expect(wrapper.get('[data-test="discover-summary"]').text()).toBe(
      'admin.supplierSources.discoverSummary',
    )
    expect(wrapper.find('[data-test="sync-result"]').exists()).toBe(false)
    expect(sync).not.toHaveBeenCalled()
  })

  it('renders every probe result and actual account change returned by sync', async () => {
    list.mockResolvedValueOnce([source])
    sync.mockResolvedValueOnce({
      source_id: 7,
      probe_results: [
        {
          client_model_id: 'deepseek-v4-pro', upstream_model_id: 'deepseek-v4-pro',
          status: 'passed', protocol: 'openai_chat_completions',
        },
        {
          client_model_id: 'qwen-3.7-max', upstream_model_id: 'qwen-3.7-max',
          status: 'passed', protocol: 'openai_chat_completions',
        },
      ],
      changes: [{
        account_id: 101, discount_band: 3, action: 'created',
        added_models: ['deepseek-v4-pro', 'qwen-3.7-max'], removed_models: [],
        priority_after: 103, schedulable_after: true,
      }],
    })
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    const result = wrapper.get('[data-test="sync-result"]').text()
    expect(result).toContain('deepseek-v4-pro')
    expect(result).toContain('qwen-3.7-max')
    expect(result).toContain('101')
    expect(result).toContain('created')
  })

  it('does not show success when a resolved sync result reports a failed step', async () => {
    list.mockResolvedValueOnce([source])
    sync.mockResolvedValueOnce({
      source_id: 7,
      probe_results: [],
      changes: [],
      failed_step: 'verify_band_3',
    })
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    const result = wrapper.get('[data-test="sync-result"]').text()
    expect(result).toContain('verify_band_3')
    expect(result).not.toContain('admin.supplierSources.syncSucceeded')
  })

  it('shows protocol_unsupported probe results from a 422 response without success wording', async () => {
    list.mockResolvedValueOnce([{ ...source, id: 9, supplier_name: 'FMGo', channel_name: 'seedance' }])
    const failedResult = {
      source_id: 9,
      probe_results: [{
        client_model_id: 'doubao-seedance-2-0-260128',
        upstream_model_id: 'feimiao-seedance-2-0-260128',
        status: 'protocol_unsupported',
        detail: 'supplier protocol unsupported',
      }],
      changes: [],
      failed_step: 'probe',
    }
    sync.mockRejectedValueOnce(Object.assign(new Error('one or more supplier models failed validation'), {
      status: 422,
      reason: 'SUPPLIER_SOURCE_PROBE_FAILED',
      data: failedResult,
    }))
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    await wrapper.get('[data-test="source-select-9"]').trigger('click')
    await wrapper.get('[data-test="sync-source"]').trigger('click')
    await flushPromises()

    const result = wrapper.get('[data-test="sync-result"]').text()
    expect(result).toContain('protocol_unsupported')
    expect(result).toContain('doubao-seedance-2-0-260128')
    expect(result).not.toContain('admin.supplierSources.syncSucceeded')
  })

  it('contains no state machine, audit, activation, pause, or account-group controls', async () => {
    list.mockResolvedValueOnce([source])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')

    const text = wrapper.text().toLowerCase()
    for (const forbidden of ['activation', 'activate', 'pause', 'audit', 'revision', 'group_ids', 'account group']) {
      expect(text).not.toContain(forbidden)
    }
    for (const testID of ['validate-source', 'activate-source', 'pause-source', 'audit-history', 'activation-preview']) {
      expect(wrapper.find(`[data-test="${testID}"]`).exists()).toBe(false)
    }
  })

  it('does not offer copy until a saved source is selected', async () => {
    list.mockResolvedValueOnce([source])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    expect(wrapper.find('[data-test="copy-source"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="editor-title"]').text()).toBe('admin.supplierSources.editorNew')

    await wrapper.get('[data-test="source-select-7"]').trigger('click')
    expect(wrapper.get('[data-test="copy-source"]').text()).toBe('admin.supplierSources.copyAsNew')
    expect(wrapper.get('[data-test="editor-title"]').text()).toBe('admin.supplierSources.editorEdit')
  })

  it('copies the selected source into a new editor without saving or exposing credentials', async () => {
    list.mockResolvedValueOnce([
      source,
      { ...source, id: 8, channel_name: 'stbl-5 (admin.supplierSources.copySuffix)' },
    ])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')

    await wrapper.get('[data-test="copy-source"]').trigger('click')
    await nextTick()

    expect(wrapper.get('[data-test="editor-title"]').text()).toBe('admin.supplierSources.editorCopy')
    expect(wrapper.get('[data-test="copy-hint"]').text()).toBe('admin.supplierSources.copyHint')
    expect(wrapper.find('[data-test="copy-source"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="sync-source"]').exists()).toBe(false)
    expect(wrapper.get('[data-test="source-select-7"]').classes()).not.toContain('border-primary-500')
    expect((wrapper.get('[data-test="supplier-name"]').element as HTMLInputElement).value).toBe('佳杰')
    expect((wrapper.get('[data-test="channel-name"]').element as HTMLInputElement).value).toBe(
      'stbl-5 (admin.supplierSources.copySuffix 2)',
    )
    expect((wrapper.get('[data-test="endpoint"]').element as HTMLInputElement).value).toBe(source.endpoint)
    expect((wrapper.get('[data-test="credential"]').element as HTMLInputElement).value).toBe('')
    expect(wrapper.get('[data-test="credential"]').attributes('required')).toBeDefined()
    expect((wrapper.get('[data-test="client-model-id"]').element as HTMLInputElement).value).toBe('deepseek-v4-pro')
    expect((wrapper.get('[data-test="purchase-ratio"]').element as HTMLInputElement).value).toBe('0.5')
    expect(create).not.toHaveBeenCalled()
    expect(update).not.toHaveBeenCalled()

    await wrapper.get('[data-test="credential"]').setValue('copied-secret')
    await wrapper.get('[data-test="save-source"]').trigger('submit')
    await flushPromises()

    expect(create).toHaveBeenCalledWith(expect.objectContaining({
      supplier_name: '佳杰',
      channel_name: 'stbl-5 (admin.supplierSources.copySuffix 2)',
      credential: 'copied-secret',
      models: source.models,
    }))
    expect(update).not.toHaveBeenCalled()
  })

  it('copies unsaved form edits into the new editor instead of the last saved source', async () => {
    list.mockResolvedValueOnce([source])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()
    await wrapper.get('[data-test="source-select-7"]').trigger('click')

    await wrapper.get('[data-test="channel-name"]').setValue('stbl-6')
    await wrapper.get('[data-test="notes"]').setValue('未保存的合同备注')
    await wrapper.get('[data-test="copy-source"]').trigger('click')
    await nextTick()

    expect((wrapper.get('[data-test="channel-name"]').element as HTMLInputElement).value).toBe(
      'stbl-6 (admin.supplierSources.copySuffix)',
    )
    expect((wrapper.get('[data-test="notes"]').element as HTMLTextAreaElement).value).toBe('未保存的合同备注')
    expect((wrapper.get('[data-test="credential"]').element as HTMLInputElement).value).toBe('')
    expect(create).not.toHaveBeenCalled()
    expect(update).not.toHaveBeenCalled()
  })

  it('filters the source list by supplier, channel, or model and reports no matches', async () => {
    list.mockResolvedValueOnce([
      source,
      {
        ...source,
        id: 9,
        supplier_name: 'FMGo',
        channel_name: 'seedance',
        models: [{
          client_model_id: 'doubao-seedance-2-0-260128',
          upstream_model_id: 'feimiao-seedance-2-0-260128',
          purchase_ratio: 0.5,
        }],
      },
    ])
    const wrapper = mount(SupplierSourcesView)
    await flushPromises()

    expect(wrapper.get('[data-test="source-list-count"]').text()).toBe('2')
    expect(wrapper.find('[data-test="source-select-7"]').exists()).toBe(true)
    expect(wrapper.find('[data-test="source-select-9"]').exists()).toBe(true)

    await wrapper.get('[data-test="source-search"]').setValue('seedance')
    await nextTick()

    expect(wrapper.get('[data-test="source-list-count"]').text()).toBe('1/2')
    expect(wrapper.find('[data-test="source-select-7"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="source-select-9"]').exists()).toBe(true)

    await wrapper.get('[data-test="source-search"]').setValue('unknown-model')
    await nextTick()

    expect(wrapper.get('[data-test="source-search-empty"]').text()).toBe('admin.supplierSources.noSearchResults')
    expect(wrapper.find('[data-test="source-select-7"]').exists()).toBe(false)
    expect(wrapper.find('[data-test="source-select-9"]').exists()).toBe(false)
  })
})
