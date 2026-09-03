import { describe, expect, it } from 'vitest'

import homeOverlay from '@/i18n/tk/home.tk'

function chinaExportCopy(locale: 'en' | 'zh') {
  return homeOverlay[locale].home.chinaExport as Record<string, unknown>
}

describe('China export homepage copy', () => {
  it('provides a localized promise for each supported locale', () => {
    expect(chinaExportCopy('en').heroTitle).toBe("China's leading AI models. One API.")
    expect(chinaExportCopy('zh').heroTitle).toBe('中国领先 AI 模型，一个 API。')
    expect(chinaExportCopy('zh')).not.toEqual(chinaExportCopy('en'))
  })

  it.each(['en', 'zh'] as const)('offers a free trial in %s without public amounts or support promises', (locale) => {
    const serialized = JSON.stringify(chinaExportCopy(locale))

    expect(serialized).toMatch(locale === 'en' ? /free trial/i : /免费试用/)
    expect(serialized).not.toMatch(/\$1|1M|100万|tokens free/i)
    expect(serialized).not.toMatch(/response time|24\/7 support|SLA/i)
  })

  it.each(['en', 'zh'] as const)('identifies the proof as Seedance 2.5 in %s', (locale) => {
    expect(chinaExportCopy(locale).proofBadge).toBe('Seedance 2.5')
  })
})
