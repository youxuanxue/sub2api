import { describe, expect, it } from 'vitest'
import {
  accountHasStoredVertexServiceAccountJson,
  buildVertexServiceAccountCredentials,
  parseVertexServiceAccountJsonInput,
  VertexServiceAccountParseError
} from '@/utils/vertexServiceAccount'

const SAMPLE_SA = {
  type: 'service_account',
  project_id: 'tk-vertex-trial',
  private_key_id: 'kid',
  private_key: '-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n',
  client_email: 'svc@tk-vertex-trial.iam.gserviceaccount.com'
}

describe('parseVertexServiceAccountJsonInput', () => {
  it('parses valid JSON and normalizes output', () => {
    const parsed = parseVertexServiceAccountJsonInput(JSON.stringify(SAMPLE_SA))
    expect(parsed.projectId).toBe('tk-vertex-trial')
    expect(parsed.clientEmail).toBe('svc@tk-vertex-trial.iam.gserviceaccount.com')
    expect(JSON.parse(parsed.normalizedJson)).toMatchObject({
      project_id: 'tk-vertex-trial',
      client_email: 'svc@tk-vertex-trial.iam.gserviceaccount.com'
    })
  })

  it('rejects missing private_key', () => {
    const bad = { ...SAMPLE_SA, private_key: '' }
    expect(() => parseVertexServiceAccountJsonInput(JSON.stringify(bad))).toThrow(
      VertexServiceAccountParseError
    )
    try {
      parseVertexServiceAccountJsonInput(JSON.stringify(bad))
    } catch (err) {
      expect(err).toBeInstanceOf(VertexServiceAccountParseError)
      expect((err as VertexServiceAccountParseError).i18nKey).toBe(
        'admin.accounts.vertexSaJsonMissingFields'
      )
    }
  })

  it('rejects invalid JSON', () => {
    expect(() => parseVertexServiceAccountJsonInput('{not-json')).toThrow(VertexServiceAccountParseError)
  })
})

describe('buildVertexServiceAccountCredentials', () => {
  it('writes canonical credential fields', () => {
    const parsed = parseVertexServiceAccountJsonInput(JSON.stringify(SAMPLE_SA))
    const creds = buildVertexServiceAccountCredentials(parsed, 'us-central1')
    expect(creds).toEqual({
      service_account_json: parsed.normalizedJson,
      project_id: 'tk-vertex-trial',
      client_email: 'svc@tk-vertex-trial.iam.gserviceaccount.com',
      location: 'us-central1',
      tier_id: 'vertex'
    })
  })
})

describe('accountHasStoredVertexServiceAccountJson', () => {
  it('prefers credentials_status when present', () => {
    expect(
      accountHasStoredVertexServiceAccountJson({}, { has_service_account_json: true })
    ).toBe(true)
    expect(
      accountHasStoredVertexServiceAccountJson({ service_account_json: 'x' }, {
        has_service_account_json: false
      })
    ).toBe(false)
  })

  it('falls back to credentials blob', () => {
    expect(
      accountHasStoredVertexServiceAccountJson({ service_account_json: '{"a":1}' }, null)
    ).toBe(true)
  })
})
