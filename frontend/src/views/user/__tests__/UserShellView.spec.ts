import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'

import UserShellView from '../UserShellView.vue'

const authState = reactive({ isAuthenticated: true })

vi.mock('vue-router', () => ({
  RouterView: defineComponent({ name: 'RouterView', template: '<div data-testid="router-view" />' }),
}))

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => authState,
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({
    name: 'AppLayout',
    template: '<div data-testid="app-layout"><slot /></div>',
  }),
}))

describe('UserShellView', () => {
  it('wraps authenticated routes in AppLayout', () => {
    authState.isAuthenticated = true
    const wrapper = mount(UserShellView)
    expect(wrapper.find('[data-testid="app-layout"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="router-view"]').exists()).toBe(true)
  })

  it('renders guest shell children without AppLayout chrome', () => {
    authState.isAuthenticated = false
    const wrapper = mount(UserShellView)
    expect(wrapper.find('[data-testid="app-layout"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="router-view"]').exists()).toBe(true)
  })
})
