import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import { ref } from 'vue'

import QuickstartView from '../QuickstartView.vue'
import type { ApiKey } from '@/types'

const { listKeys, replaceMock } = vi.hoisted(() => ({
  listKeys: vi.fn(),
  replaceMock: vi.fn(),
}))

const routeQuery = ref<Record<string, string>>({})

vi.mock('vue-router', () => ({
  useRoute: () => ({
    get query() {
      return routeQuery.value
    },
  }),
  useRouter: () => ({ replace: replaceMock }),
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key,
    }),
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    cachedPublicSettings: { api_base_url: 'https://api.example.com' },
  }),
}))

vi.mock('@/api/keys', () => ({
  list: (...args: unknown[]) => listKeys(...args),
  create: vi.fn(),
}))

vi.mock('@/components/keys/UseKeyGuide.vue', () => ({
  default: {
    name: 'UseKeyGuide',
    template: '<div data-test="use-key-guide" />',
  },
}))

const universalKey = (): ApiKey => ({
  id: 42,
  user_id: 1,
  key: 'sk-universal-test-key',
  name: 'Test',
  group_id: null,
  routing_mode: 'universal',
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2025-01-01T00:00:00Z',
  updated_at: '2025-01-01T00:00:00Z',
  current_concurrency: 0,
  rate_limit_5h: 0,
  rate_limit_1d: 0,
  rate_limit_7d: 0,
  usage_5h: 0,
  usage_1d: 0,
  usage_7d: 0,
  window_5h_start: null,
  window_1d_start: null,
  window_7d_start: null,
  reset_5h_at: null,
  reset_1d_at: null,
  reset_7d_at: null,
})

async function mountView() {
  const wrapper = mount(QuickstartView, {
    global: {
      stubs: {
        AppLayout: { template: '<div><slot /></div>' },
        LoadingSpinner: { template: '<div />' },
        GroupBadge: { template: '<span />' },
        QuickstartClientPicker: {
          props: ['heading', 'groups', 'selectedId'],
          emits: ['select'],
          template: '<div data-tk="quickstart-client-picker"><slot /></div>',
        },
      },
    },
  })
  await flushPromises()
  return wrapper
}

describe('QuickstartView page chrome', () => {
  beforeEach(() => {
    routeQuery.value = {}
    listKeys.mockReset()
    replaceMock.mockReset()
    listKeys.mockResolvedValue({
      items: [universalKey()],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    })
  })

  it('does not duplicate the route title inside page content', async () => {
    const wrapper = await mountView()
    expect(wrapper.find('h1').exists()).toBe(false)
  })
})
