import { defineComponent, h } from 'vue'
import { mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AuthActionCaptcha from '@/components/auth/AuthActionCaptcha.vue'
import type { PublicSettings } from '@/types'

const { verifyAction, reset } = vi.hoisted(() => ({
  verifyAction: vi.fn(),
  reset: vi.fn()
}))

vi.mock('@/components/CaptchaChallenge.vue', () => ({
  default: defineComponent({
    name: 'CaptchaChallenge',
    setup(_, { expose }) {
      expose({ verifyAction, reset })
      return () => h('div')
    }
  })
}))

function settings(overrides: Partial<PublicSettings> = {}): PublicSettings {
  return {
    tencent_captcha_enabled: false,
    tencent_captcha_app_id: '',
    aliyun_captcha_enabled: false,
    aliyun_captcha_scene_id: '',
    aliyun_captcha_prefix: '',
    aliyun_captcha_region: 'cn',
    ...overrides
  } as PublicSettings
}

describe('AuthActionCaptcha', () => {
  beforeEach(() => {
    verifyAction.mockReset()
    reset.mockReset()
  })

  it('does not request a proof when action captcha is disabled', async () => {
    const wrapper = mount(AuthActionCaptcha, { props: { settings: settings() } })

    await expect(wrapper.vm.acquireProof()).resolves.toBeUndefined()
    expect(verifyAction).not.toHaveBeenCalled()
  })

  it('maps Tencent action results to Tencent request fields', async () => {
    verifyAction.mockResolvedValue({ token: 'ticket', randstr: '@rand' })
    const wrapper = mount(AuthActionCaptcha, {
      props: {
        settings: settings({
          tencent_captcha_enabled: true,
          tencent_captcha_app_id: 'app-id'
        })
      }
    })

    await expect(wrapper.vm.acquireProof()).resolves.toEqual({
      tencent_captcha_ticket: 'ticket',
      tencent_captcha_randstr: '@rand'
    })
  })

  it('maps Aliyun action results to the compatibility proof field', async () => {
    verifyAction.mockResolvedValue({ token: 'captcha-param', randstr: '' })
    const wrapper = mount(AuthActionCaptcha, {
      props: {
        settings: settings({
          aliyun_captcha_enabled: true,
          aliyun_captcha_scene_id: 'scene-id',
          aliyun_captcha_prefix: 'prefix'
        })
      }
    })

    await expect(wrapper.vm.acquireProof()).resolves.toEqual({
      turnstile_token: 'captcha-param'
    })
  })

  it('returns null when the user cancels the action challenge', async () => {
    verifyAction.mockResolvedValue(null)
    const wrapper = mount(AuthActionCaptcha, {
      props: {
        settings: settings({
          tencent_captcha_enabled: true,
          tencent_captcha_app_id: 'app-id'
        })
      }
    })

    await expect(wrapper.vm.acquireProof()).resolves.toBeNull()
    wrapper.vm.reset()
    expect(reset).toHaveBeenCalledOnce()
  })
})
