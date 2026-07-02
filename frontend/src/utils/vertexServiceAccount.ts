/** Parsed + normalized Vertex service-account credentials (SSOT with backend parseVertexServiceAccountJSON). */
export interface ParsedVertexServiceAccount {
  projectId: string
  clientEmail: string
  normalizedJson: string
}

export class VertexServiceAccountParseError extends Error {
  readonly i18nKey: string

  constructor(i18nKey: string) {
    super(i18nKey)
    this.i18nKey = i18nKey
  }
}

/** Parse GCP SA JSON; throws VertexServiceAccountParseError with i18n key on failure. */
export function parseVertexServiceAccountJsonInput(raw: string): ParsedVertexServiceAccount {
  const trimmed = raw.trim()
  if (!trimmed) {
    throw new VertexServiceAccountParseError('admin.accounts.vertexSaJsonRequired')
  }
  let parsed: Record<string, unknown>
  try {
    parsed = JSON.parse(trimmed) as Record<string, unknown>
  } catch {
    throw new VertexServiceAccountParseError('admin.accounts.vertexSaJsonInvalid')
  }
  const projectId = typeof parsed.project_id === 'string' ? parsed.project_id.trim() : ''
  const clientEmail = typeof parsed.client_email === 'string' ? parsed.client_email.trim() : ''
  const privateKey = typeof parsed.private_key === 'string' ? parsed.private_key.trim() : ''
  if (!projectId || !clientEmail || !privateKey) {
    throw new VertexServiceAccountParseError('admin.accounts.vertexSaJsonMissingFields')
  }
  return {
    projectId,
    clientEmail,
    normalizedJson: JSON.stringify(parsed)
  }
}

/** Credentials blob written on create / JSON rotation on edit. */
export function buildVertexServiceAccountCredentials(
  parsed: ParsedVertexServiceAccount,
  location: string
): Record<string, unknown> {
  const loc = location.trim()
  return {
    service_account_json: parsed.normalizedJson,
    project_id: parsed.projectId,
    client_email: parsed.clientEmail,
    location: loc,
    tier_id: 'vertex'
  }
}

/** Whether stored credentials (or credentials_status) already carry SA JSON. */
export function accountHasStoredVertexServiceAccountJson(
  credentials: Record<string, unknown> | undefined | null,
  credentialsStatus?: { has_service_account_json?: boolean; has_service_account?: boolean } | null
): boolean {
  if (credentialsStatus) {
    return Boolean(credentialsStatus.has_service_account_json || credentialsStatus.has_service_account)
  }
  if (!credentials) return false
  return Boolean(credentials.service_account_json || credentials.service_account)
}
