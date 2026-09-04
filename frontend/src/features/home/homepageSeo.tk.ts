import { STOREFRONT_SEO } from '@/constants/storefrontSeo.tk'
import type { HomepageProfile } from './marketProfile.tk'

type MetaSelector = 'name' | 'property'

function setMeta(selector: MetaSelector, key: string, content: string): void {
  const element = document.head.querySelector<HTMLMetaElement>(`meta[${selector}="${key}"]`)
  element?.setAttribute('content', content)
}

export function applyHomepageSeo(profile: HomepageProfile): void {
  const seo = profile === 'china-export'
    ? STOREFRONT_SEO.chinaExport
    : {
        siteTitle: STOREFRONT_SEO.siteTitle,
        metaDescription: STOREFRONT_SEO.zh.metaDescription,
        ogDescription: STOREFRONT_SEO.zh.ogDescription,
        twitterDescription: STOREFRONT_SEO.en.twitterDescription,
        canonicalUrl: `${STOREFRONT_SEO.canonicalOrigin}/`,
        ogImageUrl: STOREFRONT_SEO.ogImageUrl,
      }
  document.title = seo.siteTitle
  setMeta('name', 'description', seo.metaDescription)
  setMeta('property', 'og:title', seo.siteTitle)
  setMeta('property', 'og:description', seo.ogDescription)
  setMeta('property', 'og:image', seo.ogImageUrl)
  setMeta('property', 'og:url', seo.canonicalUrl)
  setMeta('name', 'twitter:title', seo.siteTitle)
  setMeta('name', 'twitter:description', seo.twitterDescription)
  setMeta('name', 'twitter:image', seo.ogImageUrl)

  document.head.querySelector<HTMLLinkElement>('link[rel="canonical"]')?.setAttribute('href', seo.canonicalUrl)
}
