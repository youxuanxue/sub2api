export type HomepageProfile = 'current' | 'china-export'

export const CHINA_EXPORT_HOSTNAME = 'global.tokenkey.dev'
export const PRODUCT_ORIGIN = 'https://tokenkey.dev'

export function resolveHomepageProfile(hostname: string): HomepageProfile {
  return hostname.trim().toLowerCase().replace(/\.$/, '') === CHINA_EXPORT_HOSTNAME
    ? 'china-export'
    : 'current'
}

export function resolveGlobalProductRedirect(hostname: string, fullPath: string): string | null {
  if (resolveHomepageProfile(hostname) !== 'china-export') return null

  const path = fullPath.startsWith('/') ? fullPath : `/${fullPath}`
  const pathname = path.split(/[?#]/, 1)[0]
  if (pathname === '/' || pathname === '/home') return null

  return `${PRODUCT_ORIGIN}${path}`
}
