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

type HomeLocaleOverlay = {
  home: Record<string, unknown>
}

const en: HomeLocaleOverlay = {
  home: {
    hero: {
      title: 'Every call, official quality.',
      subtitle:
        'One API Key, all major AI models. Text, image, video. Subscription quota, predictable costs.',
    },
    tags: {
      subscriptionToApi: 'Native Access',
      nativeFidelity: 'Full Features',
      failover: 'Instant Failover',
      multiPlatform: 'Any Model',
      stickySession: 'Sticky Sessions',
      quotaControl: 'Quota Control',
    },
    cards: {
      native: {
        title: 'Official Quality, Native Pass-through',
        desc: 'Every request goes directly to official APIs. No downgrades, no dilution, no third-party routing.',
      },
      stability: {
        title: 'Full Multimodal Coverage',
        desc: 'Text, image, video. Claude / GPT / Gemini / DeepSeek. One Key for everything. Built-in Studio workspace.',
      },
      billing: {
        title: 'Subscription Quota, Predictable Costs',
        desc: 'Stop guessing your token bill. Daily/weekly/monthly subscription quota, team sharing, auto-stop on limit.',
      },
    },
    comparison: {
      title: 'Why Choose Us?',
      headers: {
        feature: 'Comparison',
        official: 'Official Direct',
        thirdParty: 'Third-party Relay',
        us: 'TokenKey',
      },
      items: {
        unified: {
          feature: 'Unified Access',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quota: {
          feature: 'Quota Management',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
        quality: {
          feature: 'Quality Guarantee',
          official: '✓',
          thirdParty: '✗',
          us: '✓',
        },
        multimodal: {
          feature: 'Multimodal Unified',
          official: '✗',
          thirdParty: 'Partial',
          us: '✓',
        },
        monitoring: {
          feature: 'Real-time Monitoring',
          official: '✗',
          thirdParty: '✗',
          us: '✓',
        },
      },
    },
    providers: {
      title: 'Supported AI Models',
      description: 'One key, switch between models freely',
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
    cta: {
      title: 'Start Free · 1M Tokens Included',
      description:
        'Enough to test your real workflow. Email only, no card required.',
    },
  },
}

const zh: HomeLocaleOverlay = {
  home: {
    hero: {
      title: '每一次调用，都是官方品质。',
      subtitle:
        '一个 API Key，所有主流 AI 模型。文本、图像、视频。订阅配额，费用可预测。',
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
    cta: {
      title: '免费试用 · 送 100 万 tokens',
      description: '足够测试你的真实工作流。只需邮箱，无需信用卡。',
    },
  },
}

export default { en, zh } as Record<'en' | 'zh', HomeLocaleOverlay>
