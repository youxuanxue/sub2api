import { describe, it, expect, beforeEach, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import OpsEmailNotificationCard from '../OpsEmailNotificationCard.vue'
import type { EmailNotificationConfig } from '@/api/admin/ops'

const mockGetEmailNotificationConfig = vi.fn()
const mockUpdateEmailNotificationConfig = vi.fn()
const mockShowError = vi.fn()
const mockShowSuccess = vi.fn()

vi.mock('@/api/admin/ops', () => ({
  opsAPI: {
    getEmailNotificationConfig: (...args: any[]) => mockGetEmailNotificationConfig(...args),
    updateEmailNotificationConfig: (...args: any[]) => mockUpdateEmailNotificationConfig(...args),
  },
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: (...args: any[]) => mockShowError(...args),
    showSuccess: (...args: any[]) => mockShowSuccess(...args),
  }),
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: { type: Boolean, default: false },
    title: { type: String, default: '' },
    width: { type: String, default: '' },
  },
  emits: ['close'],
  template: '<div v-if="show" class="base-dialog"><slot /><slot name="footer" /></div>',
})

const SelectStub = defineComponent({
  name: 'SelectControlStub',
  props: {
    modelValue: {
      type: [String, Number],
      default: '',
    },
    options: {
      type: Array,
      default: () => [],
    },
  },
  emits: ['update:modelValue'],
  methods: {
    updateValue(event: Event) {
      this.$emit('update:modelValue', (event.target as HTMLSelectElement).value)
    },
  },
  template: '<select :value="modelValue" @change="updateValue" />',
})

function sampleConfig(overrides: Partial<EmailNotificationConfig> = {}): EmailNotificationConfig {
  return {
    alert: {
      enabled: false,
      recipients: [],
      min_severity: '',
      rate_limit_per_hour: 0,
      batching_window_seconds: 0,
      include_resolved_alerts: false,
    },
    report: {
      enabled: false,
      recipients: [],
      daily_summary_enabled: false,
      daily_summary_schedule: '0 9 * * *',
      weekly_summary_enabled: false,
      weekly_summary_schedule: '0 9 * * 1',
      error_digest_enabled: false,
      error_digest_schedule: '0 9 * * *',
      error_digest_min_count: 10,
      account_health_enabled: false,
      account_health_schedule: '0 9 * * *',
      account_health_error_rate_threshold: 10,
    },
    feishu: {
      enabled: false,
      webhook_url: '',
      webhook_url_configured: false,
      signing_secret: '',
      signing_secret_configured: false,
      rate_limit_per_hour: 3,
      cooldown_seconds: 3600,
    },
    ...overrides,
  }
}

async function mountCard(config: EmailNotificationConfig) {
  mockGetEmailNotificationConfig.mockResolvedValue(config)
  const wrapper = mount(OpsEmailNotificationCard, {
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('OpsEmailNotificationCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
  })

  it('显示飞书 P0-only 提示且不回显已配置 webhook', async () => {
    const wrapper = await mountCard(sampleConfig({
      feishu: {
        enabled: true,
        webhook_url: '',
        webhook_url_configured: true,
        signing_secret: '',
        signing_secret_configured: true,
        rate_limit_per_hour: 3,
        cooldown_seconds: 3600,
      },
    }))

    expect(wrapper.text()).toContain('admin.ops.email.feishuTitle')
    expect(wrapper.text()).toContain('admin.ops.email.feishuP0OnlyHint')
    expect(wrapper.text()).not.toContain('open-apis/bot/v2/hook')

    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()
    const webhookInput = wrapper.get('[data-testid="ops-feishu-webhook-input"]')
    expect((webhookInput.element as HTMLInputElement).value).toBe('')
    expect(webhookInput.attributes('placeholder')).toBe('admin.ops.email.feishuWebhookKeepPlaceholder')
  })

  it('启用飞书但没有 webhook 时阻止保存', async () => {
    const wrapper = await mountCard(sampleConfig())
    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="ops-feishu-enabled-toggle"]').setValue(true)
    await flushPromises()

    const saveButton = wrapper.find('button.btn-primary')
    expect(saveButton.attributes('disabled')).toBeDefined()
    expect(wrapper.text()).toContain('admin.ops.email.validation.feishuWebhookRequired')
    await saveButton.trigger('click')
    expect(mockUpdateEmailNotificationConfig).not.toHaveBeenCalled()
  })

  it('使用 HTTPS webhook 保存完整 feishu payload', async () => {
    mockUpdateEmailNotificationConfig.mockImplementation(async (payload: EmailNotificationConfig) => payload)
    const wrapper = await mountCard(sampleConfig())
    await wrapper.find('button.btn-secondary').trigger('click')
    await flushPromises()

    await wrapper.get('[data-testid="ops-feishu-enabled-toggle"]').setValue(true)
    await wrapper.get('[data-testid="ops-feishu-webhook-input"]').setValue('https://open.feishu.cn/open-apis/bot/v2/hook/token')
    await wrapper.get('[data-testid="ops-feishu-signing-secret-input"]').setValue('signing-secret')
    await wrapper.get('[data-testid="ops-feishu-rate-limit-input"]').setValue(5)
    await wrapper.get('[data-testid="ops-feishu-cooldown-input"]').setValue(7200)
    await wrapper.find('button.btn-primary').trigger('click')
    await flushPromises()

    expect(mockUpdateEmailNotificationConfig).toHaveBeenCalledWith(
      expect.objectContaining({
        feishu: expect.objectContaining({
          enabled: true,
          webhook_url: 'https://open.feishu.cn/open-apis/bot/v2/hook/token',
          signing_secret: 'signing-secret',
          rate_limit_per_hour: 5,
          cooldown_seconds: 7200,
        }),
      })
    )
    expect(mockShowSuccess).toHaveBeenCalledWith('admin.ops.email.saveSuccess')
  })
})
