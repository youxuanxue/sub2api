import { describe, expect, it } from 'vitest'

import { resolveGlobalProductRedirect, resolveHomepageProfile } from '../marketProfile.tk'

describe('resolveHomepageProfile', () => {
  it.each(['tokenkey.dev', 'localhost', 'preview.tokenkey.dev', ''])('keeps %s on the current homepage', (hostname) => {
    expect(resolveHomepageProfile(hostname)).toBe('current')
  })

  it('selects the China model homepage only for the global hostname', () => {
    expect(resolveHomepageProfile('global.tokenkey.dev')).toBe('china-export')
    expect(resolveHomepageProfile('GLOBAL.TOKENKEY.DEV.')).toBe('china-export')
  })

  it('does not treat lookalike or nested hostnames as the global entry', () => {
    expect(resolveHomepageProfile('global.tokenkey.dev.example.com')).toBe('current')
    expect(resolveHomepageProfile('www.global.tokenkey.dev')).toBe('current')
  })
})

describe('resolveGlobalProductRedirect', () => {
  it('keeps both global homepage paths on the overseas host', () => {
    expect(resolveGlobalProductRedirect('global.tokenkey.dev', '/')).toBeNull()
    expect(resolveGlobalProductRedirect('global.tokenkey.dev', '/home?ref=launch#models')).toBeNull()
  })

  it('moves non-home paths to the shared product without losing query or fragment', () => {
    expect(resolveGlobalProductRedirect('global.tokenkey.dev', '/register?ref=launch#email')).toBe(
      'https://tokenkey.dev/register?ref=launch#email',
    )
  })

  it('does not redirect another hostname', () => {
    expect(resolveGlobalProductRedirect('tokenkey.dev', '/register')).toBeNull()
  })

  it('keeps network-path references on the TokenKey product origin', () => {
    expect(resolveGlobalProductRedirect('global.tokenkey.dev', '//example.com/register')).toBe(
      'https://tokenkey.dev//example.com/register',
    )
  })
})
