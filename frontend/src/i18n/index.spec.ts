import { beforeEach, describe, expect, it, vi } from 'vitest'

describe('i18n locale storage', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.resetModules()
  })

  it('migrates the legacy Sub2API locale key to TokenKey storage', async () => {
    localStorage.setItem('sub2api_locale', 'zh')

    const { getLocale } = await import('./index')

    expect(getLocale()).toBe('zh')
    expect(localStorage.getItem('tokenkey_locale')).toBe('zh')
    expect(localStorage.getItem('sub2api_locale')).toBeNull()
  })

  it('prefers the TokenKey locale key when both keys exist', async () => {
    localStorage.setItem('sub2api_locale', 'zh')
    localStorage.setItem('tokenkey_locale', 'en')

    const { getLocale } = await import('./index')

    expect(getLocale()).toBe('en')
  })
})
