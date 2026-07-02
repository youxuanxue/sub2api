/** Parsed Kiro token cache (~/.aws/sso/cache/kiro-auth-token.json). */
export type KiroAuthMethod = 'social' | 'idc'

export interface ParsedKiroTokenJson {
  accessToken: string
  refreshToken: string
  region: string
  authMethod: KiroAuthMethod
  expiresAt?: string
}

export interface ParsedKiroRegistrationJson {
  clientId: string
  clientSecret: string
}

export class KiroOAuthParseError extends Error {
  readonly i18nKey: string

  constructor(i18nKey: string) {
    super(i18nKey)
    this.i18nKey = i18nKey
  }
}

function readNonEmptyString(obj: Record<string, unknown>, ...keys: string[]): string {
  for (const key of keys) {
    const value = obj[key]
    if (typeof value === 'string' && value.trim()) {
      return value.trim()
    }
  }
  return ''
}

/** Match local_kiro_credentials.py: social only when authMethod is literally social. */
export function normalizeKiroAuthMethod(raw: string): KiroAuthMethod {
  return raw.trim().toLowerCase() === 'social' ? 'social' : 'idc'
}

export function parseKiroTokenJsonInput(raw: string): ParsedKiroTokenJson {
  const trimmed = raw.trim()
  if (!trimmed) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.tokenJsonRequired')
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.tokenJsonInvalid')
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.tokenJsonObjectRequired')
  }
  const record = parsed as Record<string, unknown>
  const accessToken = readNonEmptyString(record, 'accessToken', 'access_token')
  const refreshToken = readNonEmptyString(record, 'refreshToken', 'refresh_token')
  if (!accessToken || !refreshToken) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.tokenJsonMissingFields')
  }
  const authMethodRaw = readNonEmptyString(record, 'authMethod', 'auth_method') || 'idc'
  const region = readNonEmptyString(record, 'region') || 'us-east-1'
  const expiresAt = readNonEmptyString(record, 'expiresAt', 'expires_at') || undefined
  return {
    accessToken,
    refreshToken,
    region,
    authMethod: normalizeKiroAuthMethod(authMethodRaw),
    expiresAt
  }
}

/** IdC client registration JSON (~/.aws/sso/cache/*.json with clientId/clientSecret). */
export function parseKiroRegistrationJsonInput(raw: string): ParsedKiroRegistrationJson {
  const trimmed = raw.trim()
  if (!trimmed) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.registrationJsonRequired')
  }
  let parsed: unknown
  try {
    parsed = JSON.parse(trimmed)
  } catch {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.registrationJsonInvalid')
  }
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.registrationJsonObjectRequired')
  }
  const record = parsed as Record<string, unknown>
  const clientId = readNonEmptyString(record, 'clientId', 'client_id')
  const clientSecret = readNonEmptyString(record, 'clientSecret', 'client_secret')
  if (!clientId || !clientSecret) {
    throw new KiroOAuthParseError('admin.accounts.kiroPlatform.registrationJsonMissingFields')
  }
  return { clientId, clientSecret }
}

export function accountHasStoredKiroTokens(
  credentialsStatus?: Record<string, boolean> | null
): boolean {
  return Boolean(credentialsStatus?.has_access_token && credentialsStatus?.has_refresh_token)
}

export function accountHasStoredKiroRegistration(
  credentials?: Record<string, unknown> | null,
  credentialsStatus?: Record<string, boolean> | null
): boolean {
  if (credentialsStatus?.has_client_secret) {
    return true
  }
  if (credentialsStatus?.has_client_id) {
    return true
  }
  return Boolean(typeof credentials?.client_id === 'string' && credentials.client_id.trim())
}
