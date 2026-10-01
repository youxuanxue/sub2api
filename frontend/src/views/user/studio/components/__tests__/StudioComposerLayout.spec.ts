import { describe, expect, it } from 'vitest'
import { mount } from '@vue/test-utils'
import { createI18n } from 'vue-i18n'
import en from '@/i18n/locales/en'
import StudioModelPicker from '../StudioModelPicker.vue'
import StudioPromptTextarea from '../StudioPromptTextarea.vue'

const i18n = createI18n({
  legacy: false,
  locale: 'en',
  fallbackWarn: false,
  missingWarn: false,
  messages: { en },
})

describe('StudioModelPicker', () => {
  it('selects a compact card and keeps price/vendor/id on one secondary line', async () => {
    const wrapper = mount(StudioModelPicker, {
      props: {
        label: 'Model',
        modelValue: 'a',
        testId: 'studio-image-model',
        models: [
          {
            modelId: 'a',
            displayName: 'Nano Banana 2 (Gemini 3.1 Flash Image)',
            qualityBadgeKey: 'studio.badge.fast',
            priceLabel: '$0.0672 /img',
            vendorLabel: 'Google Gemini',
            servedId: 'gemini-3.1-flash-image',
          },
          {
            modelId: 'b',
            displayName: 'Other',
            qualityBadgeKey: 'studio.badge.standard',
            priceLabel: '$0.10 /img',
            vendorLabel: 'Other',
            servedId: 'other-model',
          },
        ],
      },
      global: { plugins: [i18n] },
    })

    const cards = wrapper.findAll('[data-testid="studio-image-model"]')
    expect(cards).toHaveLength(2)
    expect(cards[0].text()).toContain('$0.0672 /img')
    expect(cards[0].text()).toContain('gemini-3.1-flash-image')
    expect(cards[0].classes().join(' ')).toContain('py-1.5')

    await cards[1].trigger('click')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['b'])
  })
})

describe('StudioPromptTextarea', () => {
  it('defaults to a tall resizable composer so Image/Video cannot drift short', async () => {
    const wrapper = mount(StudioPromptTextarea, {
      props: {
        modelValue: 'long prompt',
        placeholder: 'Describe…',
      },
    })

    const el = wrapper.get('textarea')
    expect(el.attributes('rows')).toBe('8')
    expect(el.classes()).toContain('min-h-[10.5rem]')
    expect(el.classes()).toContain('resize-y')

    await el.setValue('edited')
    expect(wrapper.emitted('update:modelValue')?.at(-1)).toEqual(['edited'])
    expect(wrapper.emitted('input')).toHaveLength(1)
  })
})
