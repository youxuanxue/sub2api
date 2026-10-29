import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'

describe('translation HTML safety', () => {
  it('prevents escaped parameters from becoming executable iframe documents', () => {
    const i18n = createI18n({
      legacy: false,
      locale: 'en',
      warnHtmlMessage: false,
      escapeParameter: true,
      messages: { en: { frame: '<iframe srcdoc="{payload}"></iframe>' } },
    })
    // GHSA-83rm-6c8r-g728: browsers decode srcdoc and parse it as HTML again.
    const html = i18n.global.t('frame', { payload: '<img src=x onerror=alert(1)>' })
    const document = new DOMParser().parseFromString(html, 'text/html')
    expect(document.querySelector('iframe')?.hasAttribute('srcdoc')).toBe(false)
    i18n.dispose()
  })
})
