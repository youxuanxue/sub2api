import { beforeEach, describe, expect, it, vi } from 'vitest'
import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'

const { updateAccountMock, checkMixedChannelRiskMock, getWebSearchEmulationConfigMock, getSettingsMock, listTLSFingerprintProfilesMock } = vi.hoisted(() => ({
  updateAccountMock: vi.fn(),
  checkMixedChannelRiskMock: vi.fn(),
  getWebSearchEmulationConfigMock: vi.fn(),
  getSettingsMock: vi.fn(),
  listTLSFingerprintProfilesMock: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    isSimpleMode: true
  })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      update: updateAccountMock,
      checkMixedChannelRisk: checkMixedChannelRiskMock
    },
    settings: {
      getWebSearchEmulationConfig: getWebSearchEmulationConfigMock,
      getSettings: getSettingsMock
    },
    tlsFingerprintProfiles: {
      list: listTLSFingerprintProfilesMock
    }
  }
}))

vi.mock('@/api/admin/accounts', () => ({
  getAntigravityDefaultModelMapping: vi.fn()
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

import EditAccountModal from '../EditAccountModal.vue'

beforeEach(() => {
  updateAccountMock.mockReset()
  checkMixedChannelRiskMock.mockReset()
  getWebSearchEmulationConfigMock.mockReset()
  getSettingsMock.mockReset()
  listTLSFingerprintProfilesMock.mockReset()

  checkMixedChannelRiskMock.mockResolvedValue({ has_risk: false })
  getWebSearchEmulationConfigMock.mockResolvedValue({ enabled: false, providers: [] })
  getSettingsMock.mockResolvedValue({ account_quota_notify_enabled: false })
  listTLSFingerprintProfilesMock.mockResolvedValue([])
})

const BaseDialogStub = defineComponent({
  name: 'BaseDialog',
  props: {
    show: {
      type: Boolean,
      default: false
    }
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>'
})

const SelectStub = defineComponent({
  name: 'SelectStub',
  props: {
    modelValue: {
      type: [String, Number, Boolean, null],
      default: ''
    },
    options: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: `
    <select
      v-bind="$attrs"
      :value="modelValue"
      @change="$emit('update:modelValue', $event.target.value)"
    >
      <option v-for="option in options" :key="option.value" :value="option.value">
        {{ option.label }}
      </option>
    </select>
  `
})

const ModelWhitelistSelectorStub = defineComponent({
  name: 'ModelWhitelistSelector',
  props: {
    modelValue: {
      type: Array,
      default: () => []
    }
  },
  emits: ['update:modelValue'],
  template: '<div />'
})

function buildGrokAPIKeyAccount() {
  return {
    id: 7,
    name: 'Grok Upstream',
    notes: '',
    platform: 'grok',
    type: 'apikey',
    credentials: {
      base_url: 'https://upstream.example.test'
    },
    credentials_status: { has_api_key: true },
    extra: {},
    proxy_id: null,
    concurrency: 10,
    priority: 1,
    rate_multiplier: 1,
    status: 'active',
    group_ids: [],
    expires_at: null,
    auto_pause_on_expired: false
  } as any
}

function mountModal() {
  return mount(EditAccountModal, {
    props: {
      show: true,
      account: buildGrokAPIKeyAccount(),
      proxies: [],
      groups: []
    },
    global: {
      stubs: {
        BaseDialog: BaseDialogStub,
        Select: SelectStub,
        Icon: true,
        ProxySelector: true,
        GroupSelector: true,
        ModelWhitelistSelector: ModelWhitelistSelectorStub
      }
    }
  })
}

describe('EditAccountModal — Grok API-key account', () => {
  it('blocks save when the upstream base URL is cleared', async () => {
    const wrapper = mountModal()

    await wrapper.get('input[placeholder="https://api.x.ai"]').setValue('')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')

    expect(updateAccountMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })

  it('saves the explicit upstream destination while preserving the redacted key', async () => {
    updateAccountMock.mockResolvedValue(buildGrokAPIKeyAccount())
    const wrapper = mountModal()
    await wrapper.get('input[placeholder="https://api.x.ai"]').setValue(' https://upstream.example.test ')
    await wrapper.get('form#edit-account-form').trigger('submit.prevent')
    await flushPromises()
    expect(updateAccountMock).toHaveBeenCalledWith(7, expect.objectContaining({
      credentials: expect.objectContaining({ base_url: 'https://upstream.example.test' })
    }))
    expect(updateAccountMock.mock.calls[0][1].credentials).not.toHaveProperty('api_key')
    wrapper.unmount()
  })
})
