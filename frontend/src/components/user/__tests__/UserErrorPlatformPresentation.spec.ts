import { describe, expect, it, vi } from 'vitest'
import { flushPromises, mount, shallowMount } from '@vue/test-utils'
import UserErrorRequestsTable from '../UserErrorRequestsTable.vue'
import UserErrorDetailModal from '../UserErrorDetailModal.vue'
import type { UserErrorRequest, UserErrorRequestDetail } from '@/types'

const { getMyErrorDetail } = vi.hoisted(() => ({ getMyErrorDetail: vi.fn() }))

vi.mock('@/api/usage', () => ({ getMyErrorDetail }))
vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return { ...actual, useI18n: () => ({ t: (key: string) => key }) }
})

const row: UserErrorRequest = {
  id: 1,
  created_at: '2026-08-19T00:00:00Z',
  model: 'gemini-2.5-pro',
  inbound_endpoint: '/v1/messages',
  status_code: 500,
  category: 'upstream',
  platform: 'antigravity',
  message: 'failed',
  key_name: 'test',
  key_deleted: false,
}

describe('user error platform presentation', () => {
  it('shows the public Google family in the usage error table', () => {
    const wrapper = shallowMount(UserErrorRequestsTable, {
      props: { rows: [row], total: 1, loading: false, page: 1, pageSize: 20 },
      global: {
        stubs: {
          DataTable: {
            props: ['data'],
            template: '<div><slot name="cell-platform" :row="data[0]" /></div>',
          },
        },
      },
    })

    expect(wrapper.text()).toContain('Google')
    expect(wrapper.text()).not.toContain('antigravity')
  })

  it('shows the public Google family in usage error details', async () => {
    getMyErrorDetail.mockResolvedValue({ ...row, error_body: '{}' } satisfies UserErrorRequestDetail)
    const wrapper = mount(UserErrorDetailModal, {
      props: { show: false, errorId: 1 },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /></div>',
          },
        },
      },
    })

    await wrapper.setProps({ show: true })
    await flushPromises()

    expect(wrapper.text()).toContain('Google')
    expect(wrapper.text()).not.toContain('antigravity')
  })
})
