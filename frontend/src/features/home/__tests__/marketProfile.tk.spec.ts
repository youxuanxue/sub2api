import { describe, expect, it } from 'vitest'

import {
  CHINA_EXPORT_API_ORIGIN,
  CHINA_EXPORT_BRAND,
  CHINA_EXPORT_HOSTNAME,
  resolveChromeBrand,
  resolveFacade,
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

describe('resolveFacade', () => {
  it('returns CallModel chrome and china-export narrative for the overseas host', () => {
    expect(resolveFacade('callmodel.io')).toEqual({
      id: 'callmodel',
      brand: 'CallModel',
      apiOrigin: 'https://api.callmodel.io',
      humanOrigin: 'https://callmodel.io',
      homeProfile: 'china-export',
    })
  })

  it('returns TokenKey chrome for the domestic host', () => {
    expect(resolveFacade('tokenkey.dev').id).toBe('tokenkey')
    expect(resolveFacade('tokenkey.dev').brand).toBe('TokenKey')
    expect(resolveFacade('tokenkey.dev').homeProfile).toBe('current')
  })
})

describe('resolveChromeBrand', () => {
  it('forces CallModel on the overseas host even when Settings say TokenKey', () => {
    expect(resolveChromeBrand('callmodel.io', 'TokenKey')).toBe('CallModel')
  })

  it('keeps Settings site_name on the TokenKey host', () => {
    expect(resolveChromeBrand('tokenkey.dev', 'Acme Gateway')).toBe('Acme Gateway')
    expect(resolveChromeBrand('tokenkey.dev', '  ')).toBe('TokenKey')
    expect(resolveChromeBrand('localhost', null)).toBe('TokenKey')
  })
})

describe('resolveGlobalProductRedirect', () => {
  it('never kicks product paths off the CallModel host under dual-facade', () => {
    expect(resolveGlobalProductRedirect('callmodel.io', '/')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '/home?ref=launch#models')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '/register?ref=launch#email')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '/dashboard')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '/models')).toBeNull()
    expect(resolveGlobalProductRedirect('callmodel.io', '//example.com/register')).toBeNull()
  })

  it('does not redirect another hostname', () => {
    expect(resolveGlobalProductRedirect('tokenkey.dev', '/register')).toBeNull()
  })
})
