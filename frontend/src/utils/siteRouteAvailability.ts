/** Domestic prod offers API access; interactive media tools are not published.
 * Shared by navigation, quick actions and the router. Server ingress separately
 * enforces the API boundary; hiding a route is not an authorization check.
 */
export function isSiteRouteAvailable(path: string, hostname = window.location.hostname): boolean {
  if (hostname.toLowerCase().replace(/\.$/, '') !== 'api-cn.tokenkey.dev') return true
  const pathname = path.split(/[?#]/, 1)[0].replace(/\/+$/, '')
  return !['/studio', '/playground', '/batch-image', '/docs/batch-image'].some(
    root => pathname === root || pathname.startsWith(`${root}/`),
  )
}
