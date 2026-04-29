import { describe, expect, it } from 'vitest'

import en from '../locales/en'
import zh from '../locales/zh'

describe('NewAPI account locale messages', () => {
  it('does not put JSON object examples in i18n messages', () => {
    const hints = [
      en.admin.accounts.newApiPlatform.statusCodeMappingHint,
      zh.admin.accounts.newApiPlatform.statusCodeMappingHint
    ]

    for (const hint of hints) {
      expect(hint).not.toMatch(/\{[^}]*:[^}]*\}/)
    }
  })
})
