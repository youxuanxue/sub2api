import { describe, expect, it } from 'vitest'
import { createI18n } from 'vue-i18n'

import overlay from '../tk/inviteTrial.tk'

// The Invite-to-Trial overlay is deep-merged over both locales; en/zh must carry
// the identical key set or one language renders raw keys.
describe('Invite-to-Trial locale overlay', () => {
  const enKeys = Object.keys(overlay.en.admin.users.inviteTrial).sort()
  const zhKeys = Object.keys(overlay.zh.admin.users.inviteTrial).sort()

  it('has matching en/zh keys', () => {
    expect(enKeys).toEqual(zhKeys)
  })

  it('has no empty values', () => {
    for (const v of Object.values(overlay.en.admin.users.inviteTrial)) {
      expect(String(v).length).toBeGreaterThan(0)
    }
    for (const v of Object.values(overlay.zh.admin.users.inviteTrial)) {
      expect(String(v).length).toBeGreaterThan(0)
    }
  })

  // Regression guard: vue-i18n treats '@' as the linked-message operator and '|'
  // as the plural separator, so a raw '@' (e.g. an email example) throws
  // "Invalid linked format" at render and blanks the dialog. Literal '@' must be
  // escaped as {'@'}. Named params like {ok} are valid. This static scan is what
  // the object-equality checks above could not catch.
  it('has no unescaped vue-i18n metacharacters', () => {
    for (const locale of ['en', 'zh'] as const) {
      for (const [key, value] of Object.entries(overlay[locale].admin.users.inviteTrial)) {
        // Strip valid literal escapes {'...'} and named params {name}.
        const stripped = String(value)
          .replace(/\{'[^']*'\}/g, '')
          .replace(/\{[a-zA-Z][\w]*\}/g, '')
        expect(stripped, `${locale}.${key}: raw '@' — escape as {'@'}`).not.toMatch(/@/)
        expect(stripped, `${locale}.${key}: lone '|' — escape it`).not.toMatch(/\|/)
      }
    }
  })

  // Sanity: compiling each message through vue-i18n must not throw (the '@' bug
  // surfaced as a compile-time SyntaxError).
  it('every message compiles without error', () => {
    const i18n = createI18n({ legacy: false, locale: 'en', messages: {} })
    for (const locale of ['en', 'zh'] as const) {
      expect(() => i18n.global.mergeLocaleMessage(locale, overlay[locale])).not.toThrow()
    }
  })
})
