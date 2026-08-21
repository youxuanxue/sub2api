import { shallowMount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'

import Select from '@/components/common/Select.vue'
import UserEditModal from '../UserEditModal.vue'

vi.mock('vue-i18n', async (importOriginal) => ({
  ...(await importOriginal<typeof import('vue-i18n')>()),
  useI18n: () => ({ t: (key: string) => key })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { users: { update: vi.fn() } }
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showSuccess: vi.fn(), showError: vi.fn() })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copyToClipboard: vi.fn() })
}))

vi.mock('@/composables/useStepUp', () => ({
  useStepUp: () => ({ run: vi.fn() }),
  isStepUpBlocked: vi.fn(() => false),
  isStepUpCancelled: vi.fn(() => false),
  stepUpBlockReason: vi.fn()
}))

describe('UserEditModal', () => {
  it('uses the shared Select component for role selection', () => {
    const wrapper = shallowMount(UserEditModal, {
      props: {
        show: true,
        user: {
          id: 7,
          email: 'admin@example.com',
          username: 'admin',
          role: 'admin',
          concurrency: 3,
          rpm_limit: 0,
          traj_export_enabled: true
        } as any
      },
      global: {
        stubs: {
          BaseDialog: {
            props: ['show'],
            template: '<div v-if="show"><slot /><slot name="footer" /></div>'
          }
        }
      }
    })

    const roleSelect = wrapper.findComponent(Select)
    expect(roleSelect.exists()).toBe(true)
    expect(roleSelect.props('modelValue')).toBe('admin')
    expect(roleSelect.props('searchable')).toBe(false)
  })
})
