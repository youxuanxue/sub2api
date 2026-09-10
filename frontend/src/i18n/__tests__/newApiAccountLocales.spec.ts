import { describe, expect, it } from 'vitest'

import { i18n, loadLocaleMessages } from '..'

describe('NewAPI account locale messages', () => {
  it('does not put JSON object examples in loaded i18n messages', async () => {
    for (const locale of ['en', 'zh'] as const) {
      await loadLocaleMessages(locale)
      const hint = i18n.global.getLocaleMessage(locale).admin.accounts.newApiPlatform.statusCodeMappingHint
      expect(hint).toBeTruthy()
      expect(hint).not.toMatch(/\{[^}]*:[^}]*\}/)
    }
  })
})
