/**
 * Single source of truth for public storefront SEO copy shared by:
 * - frontend/index.html meta tags
 * - frontend/src/i18n/tk/home.tk.ts hero/CTA overlays
 * - backend/internal/web/prerender_seo_tk.go (kept in sync via scripts/checks/storefront-seo-alignment.py)
 */
export const STOREFRONT_SEO = {
  siteTitle: 'TokenKey - AI API Gateway',
  canonicalOrigin: 'https://api.tokenkey.dev',
  ogImagePath: '/og-cover.png',
  ogImageUrl: 'https://api.tokenkey.dev/og-cover.png',
  zh: {
    metaDescription:
      'TokenKey - AI API Gateway. 每一次调用，都是官方品质。文本、图像、视频，一个 Key 全搞定。One key for every AI model, routed direct.',
    ogDescription:
      '每一次调用，都是官方品质。一个 API Key，所有主流 AI 模型。文本、图像、视频。订阅配额，费用可预测。',
    heroTitle: '每一次调用，都是官方品质。',
    heroSubtitle: '一个 API Key，所有主流 AI 模型。文本、图像、视频。订阅配额，费用可预测。',
    freeTrialZh: '免费试用，送 100 万 tokens。足够测试你的真实工作流。只需邮箱，无需信用卡。',
    ctaDescriptionZh: '足够测试你的真实工作流。只需邮箱，无需信用卡。',
  },
  en: {
    twitterDescription: 'One API key for Claude, GPT, Gemini, and more. Direct-to-vendor quality, predictable pricing.',
    heroTitle: 'One key. Every model. Zero compromises.',
    heroSubtitle:
      'Access Claude, GPT, Gemini, and DeepSeek through a single API key — routed directly to official endpoints with built-in quota controls.',
    freeTrialEn:
      'Try free with 1M tokens on us. Enough to run your real workload end-to-end. Just an email — no credit card required.',
    ctaDescriptionEn: 'Run your real workload end-to-end. Just an email — no credit card required.',
  },
} as const

export type StorefrontSeo = typeof STOREFRONT_SEO
