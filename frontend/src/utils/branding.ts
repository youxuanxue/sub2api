import { sanitizeUrl } from '@/utils/url'

/** TokenKey product mark. `/logo.svg` remains the upstream Sub2API asset. */
export const DEFAULT_SITE_LOGO = '/logo.png'

export function resolveSiteLogo(logoUrl: string | undefined | null): string {
  return (
    sanitizeUrl(logoUrl || '', {
      allowRelative: true,
      allowDataUrl: true,
    }) || DEFAULT_SITE_LOGO
  )
}

export function updateFavicon(logoUrl: string): void {
  const sanitizedLogoUrl = resolveSiteLogo(logoUrl)

  let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
  if (!link) {
    link = document.createElement('link')
    link.rel = 'icon'
    document.head.appendChild(link)
  }

  link.type = sanitizedLogoUrl.endsWith('.svg') ? 'image/svg+xml' : 'image/png'
  link.href = sanitizedLogoUrl
}
