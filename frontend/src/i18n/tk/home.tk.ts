// TokenKey-only home/landing i18n overlay.
//
// Kept OUT of the upstream locale files (locales/en.ts, locales/zh.ts) so those
// stay near-upstream and survive merges without conflict (CLAUDE.md §5). This
// overlay is deep-merged OVER the upstream locale by i18n/index.ts via
// vue-i18n's mergeLocaleMessage — overlapping keys here win, new keys are added.
//
// Scope: only the home.* sub-trees the TokenKey landing customizes
// (tags / features / comparison / providers / cta). Everything else the landing
// renders (painPoints, docs, login, footer, …) resolves from the upstream locale.
//
// Copy discipline: value/outcome only — never expose adversarial mechanism
// (TLS fingerprint alignment, node counts, capture→diff pipeline, upstream
// auto-merge). See docs/sales for the internal, mechanism-level pitch.

import { STOREFRONT_SEO } from '@/constants/storefrontSeo.tk'

type HomeLocaleOverlay = {
  home: Record<string, unknown>
}

const en: HomeLocaleOverlay = {
  home: {
    hero: {
      title: STOREFRONT_SEO.en.heroTitle,
      subtitle: STOREFRONT_SEO.en.heroSubtitle,
    },
    tags: {
      subscriptionToApi: 'Direct Access',
      nativeFidelity: 'Full Fidelity',
      failover: 'Auto Failover',
      multiPlatform: 'Any Model',
      stickySession: 'Sticky Sessions',
      quotaControl: 'Quota Controls',
    },
    cards: {
      native: {
        title: 'Direct to Official APIs',
        desc: 'Every request hits the vendor endpoint directly. No downgrades, no third-party proxies, no quality loss.',
      },
      stability: {
        title: 'Every Modality, One Key',
        desc: 'Text, image, video, and code. Claude, GPT, Gemini, DeepSeek — all through a single API key. Built-in Studio for multimodal workflows.',
      },
      billing: {
        title: 'Predictable, Quota-based Pricing',
        desc: 'No more surprise token bills. Set daily, weekly, or monthly quotas per team. Hard caps stop spend automatically.',
      },
    },
    comparison: {
      title: 'How We Compare',
      headers: {
        feature: 'Feature',
        official: 'Vendor API',
        thirdParty: 'Third-party Relay',
        us: 'TokenKey',
      },
      items: {
        unified: {
          feature: 'Single API Key',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quota: {
          feature: 'Built-in Quotas',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quality: {
          feature: 'Vendor-grade Quality',
          official: '✓',
          thirdParty: '✗',
          us: '✓',
        },
        multimodal: {
          feature: 'Multimodal Support',
          official: '✗',
          thirdParty: 'Partial',
          us: '✓',
        },
        monitoring: {
          feature: 'Usage Monitoring',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
      },
    },
    providers: {
      title: 'Supported Models',
      description: 'One key to access any model, swap anytime',
      supported: 'Supported',
      compatible: 'Compatible',
      claude: 'Claude',
      gpt: 'GPT',
      gemini: 'Gemini',
      kiro: 'Kiro',
      deepseek: 'DeepSeek',
      volcengine: 'Doubao · VolcEngine',
      compatTitle: '200+ OpenAI-Compatible Models',
    },
    freeTrial: {
      badge: '🎁 Free Trial: 1M Tokens',
      startFree: 'Start free — no card required',
    },
    useCases: {
      title: 'Built for Every AI Workflow',
      subtitle: 'Start free. Scale to production.',
      aiCoding: {
        title: 'AI Coding Agent',
        desc: 'Power Claude Code, Cursor, Codex, and Cline with one key. No per-seat license, pay by actual usage.',
      },
      creativeStudio: {
        title: 'Creative Studio',
        desc: 'Generate images with GPT, videos with Gemini and Runway, all from a unified workspace.',
      },
      teamSharing: {
        title: 'Team API Sharing',
        desc: 'One subscription, multiple team members. Quota-controlled, usage-tracked, no overspend surprises.',
      },
    },
    faq: {
      title: 'Frequently Asked Questions',
      items: {
        differ: {
          q: 'How does TokenKey differ from third-party relay services?',
          a: 'Every request goes directly to official APIs with full feature fidelity — no third-party routing, no quality downgrades.',
        },
        models: {
          q: 'Which AI models are supported?',
          a: 'Claude, GPT, Gemini, DeepSeek, Doubao, plus 200+ OpenAI-compatible models. Text, image, and video generation.',
        },
        billing: {
          q: 'How does billing work?',
          a: 'Subscription-based quota (daily/weekly/monthly). Predictable costs, auto-stop on limit — no surprise bills.',
        },
        tools: {
          q: 'Can I use TokenKey with Claude Code / Cursor / Codex?',
          a: 'Yes — set ANTHROPIC_BASE_URL and you are ready. Native support for sticky sessions, extended thinking, and streaming.',
        },
        trial: {
          q: 'Is there a free trial?',
          a: '1M tokens free, email registration only, no credit card required. Start building immediately.',
        },
        quotaUp: {
          q: 'What happens when my quota is used up?',
          a: 'Requests pause until the next billing cycle or you top up. No hidden overage charges.',
        },
      },
    },
    cta: {
      title: 'Try Free — 1M Tokens on Us',
      description: STOREFRONT_SEO.en.ctaDescriptionEn,
    },
  },
}

const zh: HomeLocaleOverlay = {
  home: {
    hero: {
      title: STOREFRONT_SEO.zh.heroTitle,
      subtitle: STOREFRONT_SEO.zh.heroSubtitle,
    },
    tags: {
      subscriptionToApi: '原生接入',
      nativeFidelity: '特性全开',
      failover: '秒级切换',
      multiPlatform: '多模任选',
      stickySession: '会话保持',
      quotaControl: '配额可控',
    },
    cards: {
      native: {
        title: '官方品质，原生透传',
        desc: '每一次请求都直达官方 API，不降级、不掺水、不路由到第三方。',
      },
      stability: {
        title: '全模态覆盖',
        desc: '文本、图像、视频。Claude / GPT / Gemini / DeepSeek。一个 Key 全搞定。内置 Studio 创作工作台。',
      },
      billing: {
        title: '订阅配额，费用可预测',
        desc: '告别按 token 猜账单。按日/周/月订阅配额，团队共享，超限自动停。',
      },
    },
    comparison: {
      title: '为什么选择我们？',
      headers: {
        feature: '对比项',
        official: '直接调官方',
        thirdParty: '第三方中转',
        us: 'TokenKey',
      },
      items: {
        unified: {
          feature: '统一接入',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quota: {
          feature: '配额管理',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quality: {
          feature: '质量保障',
          official: '✓',
          thirdParty: '✗',
          us: '✓',
        },
        multimodal: {
          feature: '多模态统一',
          official: '✗',
          thirdParty: '部分',
          us: '✓',
        },
        monitoring: {
          feature: '实时监控',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
      },
    },
    providers: {
      title: '已支持的 AI 模型',
      description: '一个密钥，多模型随心切换',
      supported: '已支持',
      compatible: '可接入',
      claude: 'Claude',
      gpt: 'GPT',
      gemini: 'Gemini',
      kiro: 'Kiro',
      deepseek: 'DeepSeek',
      volcengine: '豆包·VolcEngine',
      compatTitle: '200+ OpenAI 兼容模型',
    },
    freeTrial: {
      badge: '🎁 免费试用：100 万 Tokens',
      startFree: '免费开始，无需信用卡',
    },
    useCases: {
      title: '为每种 AI 工作流而生',
      subtitle: '免费开始，按需扩展。',
      aiCoding: {
        title: 'AI 编程助手',
        desc: '一个 Key 驱动 Claude Code、Cursor、Codex、Cline。无需按席位付费，按实际用量计费。',
      },
      creativeStudio: {
        title: '创意工作室',
        desc: '用 GPT 生成图像，用 Gemini 和 Runway 生成视频，统一工作台一站搞定。',
      },
      teamSharing: {
        title: '团队 API 共享',
        desc: '一份订阅，多人共用。配额可控，用量可追踪，杜绝超支意外。',
      },
    },
    faq: {
      title: '常见问题',
      items: {
        differ: {
          q: 'TokenKey 和第三方中转服务有什么区别？',
          a: '每次请求都直达官方 API，特性完整透传——不路由第三方，不降级质量。',
        },
        models: {
          q: '支持哪些 AI 模型？',
          a: 'Claude、GPT、Gemini、DeepSeek、豆包，以及 200+ OpenAI 兼容模型。支持文本、图像、视频生成。',
        },
        billing: {
          q: '计费方式是怎样的？',
          a: '订阅配额制（按日/周/月），费用可预测，超限自动暂停——不会有意外账单。',
        },
        tools: {
          q: '可以用 TokenKey 接入 Claude Code / Cursor / Codex 吗？',
          a: '可以——设置 ANTHROPIC_BASE_URL 即可。原生支持会话保持、扩展思考和流式输出。',
        },
        trial: {
          q: '有免费试用吗？',
          a: '100 万 tokens 免费，仅需邮箱注册，无需绑定信用卡。立即开始使用。',
        },
        quotaUp: {
          q: '配额用完了怎么办？',
          a: '请求会暂停，直到下一个计费周期或充值。不会有隐性超额费用。',
        },
      },
    },
    cta: {
      title: '免费试用 · 送 100 万 tokens',
      description: STOREFRONT_SEO.zh.ctaDescriptionZh,
    },
  },
}

export default { en, zh } as Record<'en' | 'zh', HomeLocaleOverlay>
