import { describe, expect, it } from 'vitest'

import { i18n, loadLocaleMessages } from '..'

describe('NewAPI account locale messages', () => {
  it('does not put JSON object examples in i18n messages', async () => {
    for (const locale of ['en', 'zh'] as const) {
      await loadLocaleMessages(locale)
      const messages = i18n.global.getLocaleMessage(locale) as Record<string, any>
      const hint = messages.admin.accounts.newApiPlatform.statusCodeMappingHint
      expect(hint).not.toMatch(/\{[^}]*:[^}]*\}/)
    }
  })
})
