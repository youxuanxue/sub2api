import { describe, expect, it } from 'vitest'
import {
  KiroOAuthParseError,
  normalizeKiroAuthMethod,
  parseKiroRegistrationJsonInput,
  parseKiroTokenJsonInput,
  accountHasStoredKiroRegistration,
  accountHasStoredKiroTokens
} from '../kiroOAuthCredentials'

describe('parseKiroTokenJsonInput', () => {
  it('parses camelCase kiro-auth-token.json', () => {
    const parsed = parseKiroTokenJsonInput(
      JSON.stringify({
        accessToken: 'at-1',
        refreshToken: 'rt-1',
        region: 'us-west-2',
        authMethod: 'social'
      })
    )
    expect(parsed).toMatchObject({
      accessToken: 'at-1',
      refreshToken: 'rt-1',
      region: 'us-west-2',
      authMethod: 'social'
    })
  })

  it('parses snake_case and defaults non-social auth to idc', () => {
    const parsed = parseKiroTokenJsonInput(
      JSON.stringify({
        access_token: 'at-2',
        refresh_token: 'rt-2',
        auth_method: 'IdC'
      })
    )
    expect(parsed.authMethod).toBe('idc')
    expect(parsed.region).toBe('us-east-1')
  })

  it('rejects missing tokens', () => {
    expect(() => parseKiroTokenJsonInput('{"accessToken":"only"}')).toThrow(KiroOAuthParseError)
  })
})

describe('parseKiroRegistrationJsonInput', () => {
  it('parses clientId/clientSecret', () => {
    const parsed = parseKiroRegistrationJsonInput(
      JSON.stringify({ clientId: 'cid', clientSecret: 'csec' })
    )
    expect(parsed).toEqual({ clientId: 'cid', clientSecret: 'csec' })
  })

  it('parses snake_case registration', () => {
    const parsed = parseKiroRegistrationJsonInput(
      JSON.stringify({ client_id: 'cid2', client_secret: 'csec2' })
    )
    expect(parsed).toEqual({ clientId: 'cid2', clientSecret: 'csec2' })
  })
})

describe('normalizeKiroAuthMethod', () => {
  it('only social is social', () => {
    expect(normalizeKiroAuthMethod('social')).toBe('social')
    expect(normalizeKiroAuthMethod('idc')).toBe('idc')
    expect(normalizeKiroAuthMethod('')).toBe('idc')
  })
})

describe('accountHasStoredKiro*', () => {
  it('detects stored tokens from credentials_status', () => {
    expect(accountHasStoredKiroTokens({ has_access_token: true, has_refresh_token: true })).toBe(
      true
    )
    expect(accountHasStoredKiroTokens({ has_access_token: true })).toBe(false)
  })

  it('detects stored registration from status or credentials', () => {
    expect(accountHasStoredKiroRegistration({}, { has_client_secret: true })).toBe(true)
    expect(accountHasStoredKiroRegistration({ client_id: 'x' }, undefined)).toBe(true)
    expect(accountHasStoredKiroRegistration({}, undefined)).toBe(false)
  })
})
