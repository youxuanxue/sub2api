import { describe, expect, it } from 'vitest'

import {
  CHINA_EXPORT_API_ORIGIN,
  CHINA_EXPORT_BRAND,
  CHINA_EXPORT_HOSTNAME,
  resolveGlobalProductRedirect,
  resolveHomepageProfile,
} from '../marketProfile.tk'

describe('resolveHomepageProfile', () => {
  it.each(['tokenkey.dev', 'localhost', 'preview.tokenkey.dev', 'global.tokenkey.dev', ''])(
    'keeps %s on the current homepage',
    (hostname) => {
      expect(resolveHomepageProfile(hostname)).toBe('current')
    },
  )

  it('selects the China model homepage only for the CallModel hostname', () => {
    expect(CHINA_EXPORT_HOSTNAME).toBe('callmodel.io')
    expect(resolveHomepageProfile('callmodel.io')).toBe('china-export')
    expect(resolveHomepageProfile('CALLMODEL.IO.')).toBe('china-export')
  })

  it('does not treat lookalike or nested hostnames as the overseas entry', () => {
    expect(resolveHomepageProfile('callmodel.io.example.com')).toBe('current')
    expect(resolveHomepageProfile('www.callmodel.io')).toBe('current')
  })

  it('exposes the overseas API origin and brand for china-export surfaces', () => {
    expect(CHINA_EXPORT_API_ORIGIN).toBe('https://api.callmodel.io')
    expect(CHINA_EXPORT_BRAND).toBe('CallModel')
  })
})

describe('resolveGlobalProductRedirect', () => {
  it('keeps both overseas homepage paths on the CallModel host', () => {
    expect(resolveGlobalProductRedirect('callmodel.io', '/')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '/home?ref=launch#models')).toBeNull()
  })

  it('moves non-home paths to the shared product without losing query or fragment', () => {
    expect(resolveGlobalProductRedirect('callmodel.io', '/register?ref=launch#email')).toBe(
      'https://tokenkey.dev/register?ref=launch#email',
    )
  })

  it('does not redirect another hostname', () => {
    expect(resolveGlobalProductRedirect('tokenkey.dev', '/register')).toBeNull()
  })

  it('keeps network-path references on the TokenKey product origin', () => {
    expect(resolveGlobalProductRedirect('callmodel.io', '//example.com/register')).toBe(
      'https://tokenkey.dev//example.com/register',
    )
  })
})
