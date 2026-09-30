export type HomepageProfile = 'current' | 'china-export'
export type FacadeId = 'tokenkey' | 'callmodel'

export interface SiteFacade {
  id: FacadeId
  brand: string
  apiOrigin: string
  humanOrigin: string
  homeProfile: HomepageProfile
}

export const CHINA_EXPORT_HOSTNAME = 'callmodel.io'
export const CHINA_EXPORT_API_ORIGIN = 'https://api.callmodel.io'
export const CHINA_EXPORT_BRAND = 'CallModel'
export const PRODUCT_ORIGIN = 'https://tokenkey.dev'
export const PRODUCT_API_ORIGIN = 'https://api.tokenkey.dev'

const CALLMODEL_FACADE: SiteFacade = {
  id: 'callmodel',
  brand: CHINA_EXPORT_BRAND,
  apiOrigin: CHINA_EXPORT_API_ORIGIN,
  humanOrigin: `https://${CHINA_EXPORT_HOSTNAME}`,
  homeProfile: 'china-export',
}

const TOKENKEY_FACADE: SiteFacade = {
  id: 'tokenkey',
  brand: 'TokenKey',
  apiOrigin: PRODUCT_API_ORIGIN,
  humanOrigin: PRODUCT_ORIGIN,
  homeProfile: 'current',
}

export function normalizeHostname(hostname: string): string {
  return hostname.trim().toLowerCase().replace(/\.$/, '')
}

/** Single owner for dual-facade brand chrome + homepage narrative. */
export function resolveFacade(hostname: string): SiteFacade {
  return normalizeHostname(hostname) === CHINA_EXPORT_HOSTNAME ? CALLMODEL_FACADE : TOKENKEY_FACADE
}

export function resolveHomepageProfile(hostname: string): HomepageProfile {
  return resolveFacade(hostname).homeProfile
}

/**
 * Chrome brand for sidebar / auth / document title.
 * CallModel host always shows CallModel; other hosts keep Settings site_name.
 */
export function resolveChromeBrand(hostname: string, settingsSiteName?: string | null): string {
  const facade = resolveFacade(hostname)
  if (facade.id === 'callmodel') {
    return facade.brand
  }
  const trimmed = typeof settingsSiteName === 'string' ? settingsSiteName.trim() : ''
  return trimmed || facade.brand
}

/**
 * Former cross-facade kick to tokenkey.dev — removed under dual-facade.
 * Kept as a named no-op so call sites/tests fail closed if reintroduced incorrectly.
 */
export function resolveGlobalProductRedirect(_hostname: string, _fullPath: string): string | null {
  return null
}
