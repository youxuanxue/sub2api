import { afterEach, describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import { defineComponent, reactive } from 'vue'

import { TK_ADMIN_UI_ZOOM } from '@/constants/layout'
import AdminShellView from '../AdminShellView.vue'

const route = reactive<{ name: string; query: Record<string, string> }>({
  name: 'AdminAccounts',
  query: {}
})

vi.mock('vue-router', () => ({
  RouterView: defineComponent({ name: 'RouterView', template: '<div data-testid="router-view" />' }),
  useRoute: () => route
}))

vi.mock('@/components/layout/AppLayout.vue', () => ({
  default: defineComponent({
    name: 'AppLayout',
    template: '<div data-testid="app-layout"><slot /></div>'
  })
}))

describe('AdminShellView', () => {
  afterEach(() => {
    document.documentElement.style.zoom = ''
    route.name = 'AdminAccounts'
    route.query = {}
  })

  it('applies the admin UI zoom on mount for normal admin routes', () => {
    mount(AdminShellView)
    expect(document.documentElement.style.zoom).toBe(String(TK_ADMIN_UI_ZOOM))
  })

  it('clears zoom for ops fullscreen shellless mode', () => {
    route.name = 'AdminOps'
    route.query = { fullscreen: '1' }
    mount(AdminShellView)
    expect(document.documentElement.style.zoom).toBe('')
  })

  it('restores zoom when leaving ops fullscreen', async () => {
    route.name = 'AdminOps'
    route.query = { fullscreen: '1' }
    const wrapper = mount(AdminShellView)
    expect(document.documentElement.style.zoom).toBe('')

    route.query = {}
    await wrapper.vm.$nextTick()
    expect(document.documentElement.style.zoom).toBe(String(TK_ADMIN_UI_ZOOM))
  })

  it('clears zoom on unmount so user routes stay at 100%', () => {
    const wrapper = mount(AdminShellView)
    expect(document.documentElement.style.zoom).toBe(String(TK_ADMIN_UI_ZOOM))
    wrapper.unmount()
    expect(document.documentElement.style.zoom).toBe('')
  })
})
