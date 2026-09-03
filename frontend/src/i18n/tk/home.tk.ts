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

// The hostname owns the overseas product narrative; the saved locale only
// selects how that same promise is presented.
const chinaExportEn = {
  eyebrow: 'Built for global teams',
  heroTitle: "China's leading AI models. One API.",
  heroSubtitle: 'Seedance, Seedream, Qwen, DeepSeek, GLM and Kimi. One balance, one key, one OpenAI-compatible endpoint.',
  startFree: 'Start free',
  openQuickstart: 'Open quickstart',
  browseModels: 'Browse all models',
  noCard: 'No card required. Your account and API key work across TokenKey.',
  proofBadge: 'Seedance 2.5',
  proofAlt: 'Still frame from the official Seedance 2.5 model showcase',
  proofCaption: 'Real model output from the official Seedance showcase.',
  proofSource: 'View source',
  modelsEyebrow: 'China model matrix',
  modelsTitle: 'Start with the models you came for.',
  modelsSubtitle: 'The order changes what you discover first, not what your account can call. Every TokenKey user keeps access to the full catalog.',
  featured: 'Featured',
  models: {
    seedance: 'Video generation for cinematic motion and creative production workflows.',
    seedream: 'Image generation and editing for product, campaign and concept work.',
    qwen: 'Alibaba Cloud language and multimodal models for production applications.',
    deepseek: 'Fast, capable reasoning and chat through an OpenAI-compatible API.',
    glm: 'Zhipu AI language and multimodal models for agents and applications.',
    kimi: 'Moonshot AI models for long-context reasoning, coding and agent workflows.',
  },
  verifyEyebrow: 'Verify your key',
  verifyTitle: 'Your first response in one request.',
  verifyDescription: 'Use deepseek-chat to confirm that your key, balance and OpenAI-compatible endpoint are ready. Then switch the model ID for your production workflow.',
  verifyCta: 'Get your API key',
  copyCode: 'Copy request',
  copied: 'Copied',
  faqEyebrow: 'Free trial',
  faqTitle: 'Build before you pay.',
  creditDisclaimer: 'Start with a free trial. No credit card required.',
  faq: {
    models: {
      q: 'Which models can I use now?',
      a: 'Start with Seedance, Seedream, Qwen, DeepSeek, GLM and Kimi, then browse the full TokenKey catalog. Availability is shown in the shared model catalog and is not restricted by the homepage you used.',
    },
    credit: {
      q: 'How does the free trial work?',
      a: 'Create an eligible account to receive trial access. No credit card is required, and you can start with any model currently available to your account.',
    },
    data: {
      q: 'Where is my data processed?',
      a: 'Processing location and retention depend on the model provider selected for each request. Review the provider policy for your chosen model before sending sensitive or regulated data.',
    },
    payments: {
      q: 'How do payments and refunds work?',
      a: 'Available payment methods appear in the shared TokenKey checkout. Completed purchases credit the same USD balance used by every model. Refund eligibility follows the terms shown in the product.',
    },
  },
} as const

const chinaExportZh = {
  eyebrow: '为全球团队打造',
  heroTitle: '中国领先 AI 模型，一个 API。',
  heroSubtitle: 'Seedance、Seedream、通义千问、DeepSeek、GLM 和 Kimi。共用余额、统一 Key、兼容 OpenAI 的接口。',
  startFree: '免费试用',
  openQuickstart: '打开快速开始',
  browseModels: '浏览全部模型',
  noCard: '无需信用卡。你的账户和 API Key 可在 TokenKey 全站通用。',
  proofBadge: 'Seedance 2.5',
  proofAlt: '字节跳动官方 Seedance 2.5 模型演示视频画面',
  proofCaption: '字节跳动官方 Seedance 演示中的真实模型输出。',
  proofSource: '查看来源',
  modelsEyebrow: '中国模型矩阵',
  modelsTitle: '先用你正在寻找的模型。',
  modelsSubtitle: '首页顺序只决定你先看到什么，不限制账户可调用的模型。每个 TokenKey 用户都可访问完整模型目录。',
  featured: '主推',
  models: {
    seedance: '面向电影感运动和创意制作工作流的视频生成。',
    seedream: '面向产品、营销和概念设计的图像生成与编辑。',
    qwen: '面向生产应用的阿里云语言与多模态模型。',
    deepseek: '通过兼容 OpenAI 的 API 使用高效的推理和对话模型。',
    glm: '面向智能体和应用的智谱 AI 语言与多模态模型。',
    kimi: '面向长上下文推理、编程和智能体工作流的月之暗面模型。',
  },
  verifyEyebrow: '验证你的 Key',
  verifyTitle: '一次请求，拿到首个响应。',
  verifyDescription: '用 deepseek-chat 确认 API Key、余额和兼容 OpenAI 的接口已就绪，然后仅需替换模型 ID 即可开始生产工作流。',
  verifyCta: '获取 API Key',
  copyCode: '复制请求',
  copied: '已复制',
  faqEyebrow: '免费试用',
  faqTitle: '先构建，后付费。',
  creditDisclaimer: '免费开始试用，无需信用卡。',
  faq: {
    models: {
      q: '现在可以使用哪些模型？',
      a: '可以从 Seedance、Seedream、通义千问、DeepSeek、GLM 和 Kimi 开始，再浏览 TokenKey 完整模型目录。模型可用性以共用目录为准，不受你访问的首页限制。',
    },
    credit: {
      q: '免费试用如何开通？',
      a: '创建符合条件的账户即可获得试用权益，无需信用卡，并可从账户当前可用的任意模型开始。',
    },
    data: {
      q: '数据在哪里处理？',
      a: '处理位置和保留方式取决于每次请求选择的模型提供商。发送敏感或受监管数据前，请查阅所选模型提供商的政策。',
    },
    payments: {
      q: '付款和退款如何处理？',
      a: '可用的付款方式会显示在 TokenKey 共用收银台中。购买完成后会充入所有模型共用的 USD 余额，退款条件以产品内展示的条款为准。',
    },
  },
} as const

const en: HomeLocaleOverlay = {
  home: {
    chinaExport: chinaExportEn,
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
        desc: 'Text, image, video, and code. Claude, GPT, Gemini, Qwen — all through a single API key. Built-in Studio for multimodal workflows.',
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
      qwen: 'Alibaba Cloud Qwen',
      compatTitle: '100+ Models',
      compatTagline: 'Full protocol matrix',
      compatProtocolMessages: 'Messages',
      compatProtocolChat: 'Chat',
      compatProtocolResponses: 'Responses',
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
          a: 'Claude, GPT, Gemini, Qwen, plus 100+ curated models on a full protocol matrix—Anthropic Messages, OpenAI Chat Completions, Responses, and more. Text, image, and video.',
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
    chinaExport: chinaExportZh,
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
        desc: '文本、图像、视频。Claude / GPT / Gemini / 通义千问。一个 Key 全搞定。内置 Studio 创作工作台。',
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
      qwen: '通义千问',
      compatTitle: '100+ 模型',
      compatTagline: '全协议矩阵',
      compatProtocolMessages: 'Messages',
      compatProtocolChat: 'Chat',
      compatProtocolResponses: 'Responses',
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
          a: 'Claude、GPT、Gemini、通义千问等一级平台，外加 100+ 精选模型；覆盖 Anthropic Messages、OpenAI Chat Completions、Responses 等全协议矩阵，文本 / 图像 / 视频一体接入。',
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
