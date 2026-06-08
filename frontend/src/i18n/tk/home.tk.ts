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
        title: 'Native, Uncompromised',
        desc: 'Swap your base URL and go — 1M context, extended thinking, prompt caching and count_tokens all pass through, untouched.',
      },
      stability: {
        title: 'Always Online',
        desc: 'Pooled across accounts and nodes. If one is rate-limited, traffic fails over in seconds — long tasks never break.',
      },
      billing: {
        title: 'Pay As You Go',
        desc: 'Usage-based billing with quota limits. Full visibility into team consumption.',
      },
    },
    comparison: {
      title: 'Why Choose Us?',
      headers: {
        feature: 'Comparison',
        official: 'Official Subscriptions',
        us: 'TokenKey',
      },
      items: {
        pricing: {
          feature: 'Pricing',
          official: 'Fixed monthly fee, pay even if unused',
          us: 'Pay only for what you use',
        },
        models: {
          feature: 'Model Selection',
          official: 'Single provider only',
          us: 'One key across platforms, 200+ compatible upstreams',
        },
        management: {
          feature: 'Account Management',
          official: 'Manage each service separately',
          us: 'Unified key, one dashboard',
        },
        stability: {
          feature: 'Stability',
          official: 'Single account rate limits',
          us: 'Pooled across nodes, auto-failover in seconds',
        },
        native: {
          feature: 'Native Capabilities',
          official: 'Capped by subscription tier',
          us: '1M context / extended thinking / prompt caching, just swap base URL',
        },
        catalog: {
          feature: 'Servable Models',
          official: 'Listed as claimed',
          us: 'Empirically probed — only models that actually return 200',
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
      title: "We don't sell accounts. We sell infrastructure.",
      description:
        "The tool you depend on shouldn't vanish mid-task. We treat AI-gateway reliability as an engineering problem.",
    },
  },
}

const zh: HomeLocaleOverlay = {
  home: {
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
        title: '原生保真',
        desc: '换 base_url 即用，1M 上下文、深度思考、缓存、count_tokens 全程透传，原汁原味。',
      },
      stability: {
        title: '稳定不掉线',
        desc: '多账号、多节点池化调度，任一被限流秒级切到下一个，长任务不中断。',
      },
      billing: {
        title: '用多少付多少',
        desc: '按实际使用量计费，支持设置配额上限，团队用量一目了然。',
      },
    },
    comparison: {
      title: '为什么选择我们？',
      headers: {
        feature: '对比项',
        official: '官方订阅',
        us: 'TokenKey',
      },
      items: {
        pricing: {
          feature: '付费方式',
          official: '固定月费，用不完也付',
          us: '按量付费，用多少付多少',
        },
        models: {
          feature: '模型选择',
          official: '单一服务商',
          us: '多平台一个密钥，200+ 兼容上游随心切换',
        },
        management: {
          feature: '账号管理',
          official: '每个服务单独管理',
          us: '统一密钥，一站管理',
        },
        stability: {
          feature: '服务稳定性',
          official: '单账号易触发限制',
          us: '多节点池化，秒级自动切换不掉线',
        },
        native: {
          feature: '原生能力',
          official: '受订阅档位限制',
          us: '1M 长上下文 / 深度思考 / 缓存全透传，换 base_url 即用',
        },
        catalog: {
          feature: '可服务模型',
          official: '列表靠标称',
          us: '实测目录，逐模型真实跑通才上架',
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
      title: '我们卖的不是号，是基础设施。',
      description: '你依赖的工具，不该在你干到一半时消失。我们把 AI 接口的稳定，当成一个工程问题来解。',
    },
  },
}

export default { en, zh } as Record<'en' | 'zh', HomeLocaleOverlay>
