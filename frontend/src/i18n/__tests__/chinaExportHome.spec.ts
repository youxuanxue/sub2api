import { describe, expect, it } from 'vitest'

import homeOverlay from '@/i18n/tk/home.tk'

function chinaExportCopy(locale: 'en' | 'zh') {
  return homeOverlay[locale].home.chinaExport as Record<string, unknown>
}

describe('China export homepage copy', () => {
  it('keeps one English promise across saved locales', () => {
    expect(chinaExportCopy('zh')).toBe(chinaExportCopy('en'))
  })

  it('qualifies the free credit claim without promising support response times', () => {
    const serialized = JSON.stringify(chinaExportCopy('en'))

    expect(serialized).toContain('$1 free credit - up to 1M DeepSeek tokens')
    expect(serialized).toContain('Actual usage varies by model and input/output mix')
    expect(serialized).not.toMatch(/response time|24\/7 support|SLA/i)
  })
})
