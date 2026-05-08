import { i18n } from '@/i18n'

/**
 * 统一生成页面标题，避免多处写入 document.title 产生覆盖冲突。
 * 优先使用 titleKey 通过 i18n 翻译，fallback 到静态 routeTitle。
 */
const tokenKeyDefaultSiteName = 'TokenKey'

function normalizeSiteName(siteName?: string): string {
  const trimmed = typeof siteName === 'string' ? siteName.trim() : ''
  if (!trimmed || trimmed.toLowerCase() === 'sub2api') return tokenKeyDefaultSiteName
  return trimmed
}

export function resolveDocumentTitle(routeTitle: unknown, siteName?: string, titleKey?: string): string {
  const normalizedSiteName = normalizeSiteName(siteName)

  if (typeof titleKey === 'string' && titleKey.trim()) {
    const translated = i18n.global.t(titleKey)
    if (translated && translated !== titleKey) {
      return `${translated} - ${normalizedSiteName}`
    }
  }

  if (typeof routeTitle === 'string' && routeTitle.trim()) {
    return `${routeTitle.trim()} - ${normalizedSiteName}`
  }

  return normalizedSiteName
}
