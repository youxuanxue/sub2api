import { describe, expect, it, vi, beforeEach } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'
import AccountUsageCell from '../AccountUsageCell.vue'
import type { Account } from '@/types'

const { getUsage } = vi.hoisted(() => ({
  getUsage: vi.fn()
}))

vi.mock('@/api/admin', () => ({
  adminAPI: {
    accounts: {
      getUsage
    }
  }
}))

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => key
    })
  }
})

function makeAccount(overrides: Partial<Account>): Account {
  return {
    id: 1,
    name: 'account',
    platform: 'antigravity',
    type: 'oauth',
    proxy_id: null,
    concurrency: 1,
    priority: 1,
    status: 'active',
    error_message: null,
    last_used_at: null,
    expires_at: null,
    auto_pause_on_expired: true,
    created_at: '2026-03-15T00:00:00Z',
    updated_at: '2026-03-15T00:00:00Z',
    schedulable: true,
    rate_limited_at: null,
    rate_limit_reset_at: null,
    overload_until: null,
    temp_unschedulable_until: null,
    temp_unschedulable_reason: null,
    session_window_start: null,
    session_window_end: null,
    session_window_status: null,
    ...overrides,
  }
}

describe('AccountUsageCell', () => {
  beforeEach(() => {
    getUsage.mockReset()
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockImplementation(() => ({
        matches: true,
        media: '(min-width: 768px)',
        onchange: null,
        addListener: vi.fn(),
        removeListener: vi.fn(),
        addEventListener: vi.fn(),
        removeEventListener: vi.fn(),
        dispatchEvent: vi.fn(),
      }))
    })
  })

  it('Antigravity 图片用量会聚合新旧 image 模型', async () => {
    getUsage.mockResolvedValue({
      antigravity_quota: {
        'gemini-2.5-flash-image': {
          utilization: 45,
          reset_time: '2026-03-01T11:00:00Z'
        },
        'gemini-3.1-flash-image': {
          utilization: 20,
          reset_time: '2026-03-01T10:00:00Z'
        },
        'gemini-3-pro-image': {
          utilization: 70,
          reset_time: '2026-03-01T09:00:00Z'
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 1001,
          platform: 'antigravity',
          type: 'oauth',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ resetsAt }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.usageWindow.gemini3Image|70|2026-03-01T09:00:00Z')
  })

  it('Antigravity 会显示 AI Credits 余额信息', async () => {
    getUsage.mockResolvedValue({
      ai_credits: [
        {
          credit_type: 'GOOGLE_ONE_AI',
          amount: 25,
          minimum_balance: 5
        }
      ]
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 1002,
          platform: 'antigravity',
          type: 'oauth',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: true,
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(wrapper.text()).toContain('admin.accounts.aiCreditsBalance')
    expect(wrapper.text()).toContain('25')
  })


  it('OpenAI OAuth 快照已过期时首屏会重新请求 usage', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 15,
        resets_at: '2026-03-08T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      },
      seven_day: {
        utilization: 77,
        resets_at: '2026-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2000,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2026-03-07T00:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2026-03-08T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2026-03-13T12:00:00Z'
          }
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2000, 'passive')
    expect(wrapper.text()).toContain('5h|15|300')
    expect(wrapper.text()).toContain('7d|77|300')
  })

  it('OpenAI OAuth 有 codex 快照时仍然使用 /usage API 数据渲染', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 18,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      },
      seven_day: {
        utilization: 36,
        resets_at: '2099-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2001,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2099-03-07T10:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2099-03-07T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2099-03-13T12:00:00Z'
          }
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2001, 'passive')
    // 单一数据源：始终使用被动 /usage API 返回值，忽略行内 codex 快照
    expect(wrapper.text()).toContain('5h|18|900')
    expect(wrapper.text()).toContain('7d|36|900')
  })

  it('OpenAI OAuth 有现成快照时，手动刷新信号会触发 usage 重拉', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 18,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      },
      seven_day: {
        utilization: 36,
        resets_at: '2099-03-13T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 9,
          tokens: 900,
          cost: 0.09,
          standard_cost: 0.09,
          user_cost: 0.09
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2010,
          platform: 'openai',
          type: 'oauth',
          extra: {
            codex_usage_updated_at: '2099-03-07T10:00:00Z',
            codex_5h_used_percent: 12,
            codex_5h_reset_at: '2099-03-07T12:00:00Z',
            codex_7d_used_percent: 34,
            codex_7d_reset_at: '2099-03-13T12:00:00Z'
          },
          rate_limit_reset_at: null
        }),
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()
    // mount 时已经拉取一次
    expect(getUsage).toHaveBeenCalledTimes(1)

    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()

    // 手动刷新再拉一次
    expect(getUsage).toHaveBeenCalledTimes(2)
    expect(getUsage).toHaveBeenCalledWith(2010, 'passive')
    // 单一数据源：始终使用被动 /usage API 值
    expect(wrapper.text()).toContain('5h|18|900')
  })

  it('OpenAI OAuth 有列表 override 时不自动拉取，手动刷新也交给 batch', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 21,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      },
      seven_day: null
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2011,
          platform: 'openai',
          type: 'oauth',
          extra: {}
        }),
        usageOverride: null,
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'windowStats'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()

    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()

    expect(getUsage).not.toHaveBeenCalled()
  })

  it('Kiro 有列表 override 时不自动拉取，但手动刷新会拉 active usage', async () => {
    getUsage.mockResolvedValue({
      kiro_usage: {
        current: 12,
        limit: 100,
        percent: 12,
        next_reset_date: '2099-03-07T12:00:00Z',
        subscription_title: 'Kiro Pro'
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2013,
          platform: 'kiro',
          type: 'oauth',
          extra: {}
        }),
        usageOverride: null,
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'resetsAt', 'color'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ resetsAt }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()
    expect(getUsage).not.toHaveBeenCalled()

    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()

    expect(getUsage).toHaveBeenCalledTimes(1)
    expect(getUsage).toHaveBeenCalledWith(2013, 'active', true)
    expect(wrapper.text()).toContain('admin.accounts.usageWindow.kiroCredits|12|2099-03-07T12:00:00Z')
  })

  it('OpenAI 列表 batch override 从 null 更新为数据时会渲染 5h/7d 窗口', async () => {
    const passiveUsage = {
      source: 'passive',
      five_hour: {
        utilization: 42,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 3,
          tokens: 300,
          cost: 0.03,
          standard_cost: 0.03,
          user_cost: 0.03
        }
      },
      seven_day: null
    }

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2014,
          platform: 'openai',
          type: 'oauth',
          extra: {}
        }),
        usageOverride: null,
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'windowStats'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    expect(wrapper.text()).toContain('-')
    expect(getUsage).not.toHaveBeenCalled()

    await wrapper.setProps({ usageOverride: passiveUsage })
    await flushPromises()

    expect(wrapper.text()).toContain('5h|42|300')
    expect(getUsage).not.toHaveBeenCalled()
  })

  it('Grok 平台复用 5h/7d usage 窗口展示本地统计', async () => {
    getUsage.mockResolvedValue({
      source: 'passive',
      five_hour: {
        utilization: 0,
        resets_at: null,
        remaining_seconds: 0,
        window_stats: {
          requests: 4,
          tokens: 4096,
          cost: 0.12,
          standard_cost: 0.12,
          user_cost: 0.10
        }
      },
      seven_day: {
        utilization: 0,
        resets_at: null,
        remaining_seconds: 0,
        window_stats: {
          requests: 9,
          tokens: 8192,
          cost: 0.24,
          standard_cost: 0.24,
          user_cost: 0.20
        }
      },
      upstream_quota: {
        provider: 'grok',
        source: 'headers',
        state: 'observed',
        subscription_tier: 'supergrok-heavy',
        dimensions: [
          {
            key: 'grok_requests',
            label: 'Requests',
            unit: 'requests',
            remaining: 800,
            limit: 900
          },
          {
            key: 'grok_tokens',
            label: 'Tokens',
            unit: 'tokens',
            remaining: 14_000_000,
            limit: 15_000_000
          }
        ]
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2014,
          platform: 'grok',
          type: 'apikey',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'windowStats'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true,
          OpenAIQuotaResetCell: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2014, 'passive')
    expect(wrapper.text()).toContain('5h|0|4096')
    expect(wrapper.text()).toContain('7d|0|8192')
    expect(wrapper.text()).toContain('grok')
    expect(wrapper.text()).toContain('supergrok-heavy')
    expect(wrapper.text()).toContain('Requests 800/900')
    expect(wrapper.text()).toContain('Tokens 14.0M/15.0M')
  })

  it('NewAPI Vertex 账号通过本地窗口 adapter 展示 5h/7d 统计', async () => {
    getUsage.mockResolvedValue({
      source: 'passive',
      five_hour: {
        utilization: 0,
        resets_at: null,
        remaining_seconds: 0,
        window_stats: {
          requests: 2,
          tokens: 2048,
          cost: 0.18,
          standard_cost: 0.18,
          user_cost: 0.18
        }
      },
      seven_day: {
        utilization: 0,
        resets_at: null,
        remaining_seconds: 0,
        window_stats: {
          requests: 7,
          tokens: 8192,
          cost: 0.72,
          standard_cost: 0.72,
          user_cost: 0.72
        }
      }
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2015,
          platform: 'newapi',
          type: 'service_account',
          channel_type: 41,
          credentials: {
            project_id: 'tk-vertex-trial',
            location: 'us-central1'
          },
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'windowStats'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true,
          OpenAIQuotaResetCell: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2015, 'passive')
    expect(wrapper.text()).toContain('5h|0|2048')
    expect(wrapper.text()).toContain('7d|0|8192')
  })

  it('Antigravity apikey relay 账号通过本地窗口 adapter 展示 5h/7d 统计', async () => {
    getUsage.mockResolvedValue({
      source: 'passive',
      five_hour: {
        utilization: 0,
        resets_at: null,
        remaining_seconds: 0,
        window_stats: {
          requests: 3,
          tokens: 3072,
          cost: 0.21,
          standard_cost: 0.21,
          user_cost: 0.21
        }
      },
      seven_day: null
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2016,
          platform: 'antigravity',
          type: 'apikey',
          extra: {}
        })
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization', 'windowStats'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
          },
          AccountQuotaInfo: true,
          OpenAIQuotaResetCell: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).toHaveBeenCalledWith(2016, 'passive')
    expect(wrapper.text()).toContain('5h|0|3072')
  })

  it('Anthropic OAuth 有列表 override 时手动刷新不回退为逐行 usage 请求', async () => {
    getUsage.mockResolvedValue({
      five_hour: {
        utilization: 50,
        resets_at: '2099-03-07T12:00:00Z',
        remaining_seconds: 3600,
        window_stats: {
          requests: 5,
          tokens: 500,
          cost: 0.05,
          standard_cost: 0.05,
          user_cost: 0.05
        }
      },
      seven_day: null
    })

    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2012,
          platform: 'anthropic',
          type: 'oauth',
          extra: {}
        }),
        usageOverride: null,
        manualRefreshToken: 0
      },
      global: {
        stubs: {
          UsageProgressBar: true,
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()
    await wrapper.setProps({ manualRefreshToken: 1 })
    await flushPromises()

    expect(getUsage).not.toHaveBeenCalled()
  })

  it('Anthropic OAuth 不在上游摘要里重复展示已渲染的 5h/7d 窗口', async () => {
    const wrapper = mount(AccountUsageCell, {
      props: {
        account: makeAccount({
          id: 2017,
          platform: 'anthropic',
          type: 'oauth',
          extra: {}
        }),
        usageOverride: {
          source: 'passive',
          updated_at: null,
          five_hour: {
            utilization: 0,
            resets_at: '2099-03-07T12:00:00Z',
            remaining_seconds: 3600
          },
          seven_day: {
            utilization: 19,
            resets_at: '2099-03-13T12:00:00Z',
            remaining_seconds: 3600
          },
          seven_day_sonnet: {
            utilization: 91,
            resets_at: '2099-03-13T12:00:00Z',
            remaining_seconds: 3600
          },
          upstream_quota: {
            provider: 'anthropic',
            source: 'passive',
            state: 'observed',
            subscription_tier: 'Max',
            dimensions: [
              { key: 'anthropic_5h', label: '5h', window: '5h', utilization: 0 },
              { key: 'anthropic_7d', label: '7d', window: '7d', utilization: 19 },
              { key: 'anthropic_7d_sonnet', label: '7d Sonnet', window: '7d', utilization: 91 }
            ]
          }
        }
      },
      global: {
        stubs: {
          UsageProgressBar: {
            props: ['label', 'utilization'],
            template: '<div class="usage-bar">{{ label }}|{{ utilization }}</div>'
          },
          AccountQuotaInfo: true
        }
      }
    })

    await flushPromises()

    expect(getUsage).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('5h|0')
    expect(wrapper.text()).toContain('7d|19')
    expect(wrapper.text()).toContain('7d S|91')
    expect(wrapper.text()).toContain('anthropic')
    expect(wrapper.text()).toContain('Max')
    expect(wrapper.text()).not.toContain('5h 5h 0%')
    expect(wrapper.text()).not.toContain('7d 7d 19%')
    expect(wrapper.text()).not.toContain('7d Sonnet 7d 91%')
  })

  it('OpenAI OAuth 在无 codex 快照时会回退显示 usage 接口窗口', async () => {
	getUsage.mockResolvedValue({
	  five_hour: {
	    utilization: 0,
	    resets_at: null,
	    remaining_seconds: 0,
	    window_stats: {
	      requests: 2,
	      tokens: 27700,
	      cost: 0.06,
	      standard_cost: 0.06,
	      user_cost: 0.06
	    }
	  },
	  seven_day: {
	    utilization: 0,
	    resets_at: null,
	    remaining_seconds: 0,
	    window_stats: {
	      requests: 2,
	      tokens: 27700,
	      cost: 0.06,
	      standard_cost: 0.06,
	      user_cost: 0.06
	    }
	  }
	})

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2002,
		      platform: 'openai',
		      type: 'oauth',
		      extra: {}
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()

	expect(getUsage).toHaveBeenCalledWith(2002, 'passive')
	expect(wrapper.text()).toContain('5h|0|27700')
	expect(wrapper.text()).toContain('7d|0|27700')
  })

  it('OpenAI OAuth 在行数据刷新但仍无 codex 快照时会重新拉取 usage', async () => {
	getUsage
	  .mockResolvedValueOnce({
	    five_hour: {
	      utilization: 0,
	      resets_at: null,
	      remaining_seconds: 0,
	      window_stats: {
	        requests: 1,
	        tokens: 100,
	        cost: 0.01,
	        standard_cost: 0.01,
	        user_cost: 0.01
	      }
	    },
	    seven_day: null
	  })
	  .mockResolvedValueOnce({
	    five_hour: {
	      utilization: 0,
	      resets_at: null,
	      remaining_seconds: 0,
	      window_stats: {
	        requests: 2,
	        tokens: 200,
	        cost: 0.02,
	        standard_cost: 0.02,
	        user_cost: 0.02
	      }
	    },
	    seven_day: null
	  })

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2003,
		      platform: 'openai',
		      type: 'oauth',
		      updated_at: '2026-03-07T10:00:00Z',
		      extra: {}
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()
	expect(wrapper.text()).toContain('5h|0|100')
	expect(getUsage).toHaveBeenCalledTimes(1)

	await wrapper.setProps({
	  account: {
	    id: 2003,
	    platform: 'openai',
	    type: 'oauth',
	    updated_at: '2026-03-07T10:01:00Z',
	    extra: {}
	  }
	})

	await flushPromises()
	expect(getUsage).toHaveBeenCalledTimes(2)
	expect(wrapper.text()).toContain('5h|0|200')
  })

  it('OpenAI OAuth 已限额时显示 /usage API 返回的限额数据', async () => {
	getUsage.mockResolvedValue({
	  five_hour: {
	    utilization: 100,
	    resets_at: '2026-03-07T12:00:00Z',
	    remaining_seconds: 3600,
	    window_stats: {
	      requests: 211,
	      tokens: 106540000,
	      cost: 38.13,
	      standard_cost: 38.13,
	      user_cost: 38.13
	    }
	  },
	  seven_day: {
	    utilization: 100,
	    resets_at: '2026-03-13T12:00:00Z',
	    remaining_seconds: 3600,
	    window_stats: {
	      requests: 211,
	      tokens: 106540000,
	      cost: 38.13,
	      standard_cost: 38.13,
	      user_cost: 38.13
	    }
	  }
	})

		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 2004,
		      platform: 'openai',
		      type: 'oauth',
		      rate_limit_reset_at: '2099-03-07T12:00:00Z',
		      extra: {
		        codex_5h_used_percent: 0,
		        codex_7d_used_percent: 0
		      }
		    })
		  },
	  global: {
	    stubs: {
	      UsageProgressBar: {
	        props: ['label', 'utilization', 'resetsAt', 'windowStats', 'color'],
	        template: '<div class="usage-bar">{{ label }}|{{ utilization }}|{{ windowStats?.tokens }}</div>'
	      },
	      AccountQuotaInfo: true
	    }
	  }
	})

	await flushPromises()

  expect(getUsage).toHaveBeenCalledWith(2004, 'passive')
  expect(wrapper.text()).toContain('5h|100|106540000')
  expect(wrapper.text()).toContain('7d|100|106540000')
  })

  it('Key 账号会展示 today stats 徽章并带 A/U 提示', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3001,
		      platform: 'anthropic',
		      type: 'apikey'
		    }),
		    todayStats: {
		      requests: 1_000_000,
		      tokens: 1_000_000_000,
		      cost: 12.345,
		      standard_cost: 12.345,
		      user_cost: 6.789
		    }
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.text()).toContain('1.0M req')
		expect(wrapper.text()).toContain('1.0B')
		expect(wrapper.text()).toContain('A $12.35')
		expect(wrapper.text()).toContain('U $6.79')

		const badges = wrapper.findAll('span[title]')
		expect(badges.some(node => node.attributes('title') === 'usage.accountBilled')).toBe(true)
		expect(badges.some(node => node.attributes('title') === 'usage.userBilled')).toBe(true)
  })

  it('Key 账号在 today stats loading 时显示骨架屏', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3002,
		      platform: 'anthropic',
		      type: 'apikey'
		    }),
		    todayStats: null,
		    todayStatsLoading: true
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.findAll('.animate-pulse').length).toBeGreaterThan(0)
  })

  it('Key 账号在无 today stats 且无配额时显示兜底短横线', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 3003,
		      platform: 'anthropic',
		      type: 'apikey',
		      quota_limit: 0,
		      quota_daily_limit: 0,
		      quota_weekly_limit: 0
		    }),
		    todayStats: null,
		    todayStatsLoading: false
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.text().trim()).toBe('-')
  })

  it('Vertex 账号会在 Gemini 用量窗口里展示 today stats 徽章', async () => {
		const wrapper = mount(AccountUsageCell, {
		  props: {
		    account: makeAccount({
		      id: 4001,
		      platform: 'gemini',
		      type: 'service_account',
          credentials: {
            tier_id: 'vertex',
            project_id: 'vertex-proj',
            client_email: 'svc@vertex-proj.iam.gserviceaccount.com',
            location: 'global'
          },
		      extra: {}
		    }),
		    todayStats: {
		      requests: 0,
		      tokens: 0,
		      cost: 0,
		      standard_cost: 0,
		      user_cost: 0
		    }
		  },
		  global: {
		    stubs: {
		      UsageProgressBar: true,
		      AccountQuotaInfo: true
		    }
		  }
		})

		await flushPromises()

		expect(wrapper.text()).toContain('0 req')
		expect(wrapper.text()).toContain('0')
		expect(wrapper.text()).toContain('A $0.00')
		expect(wrapper.text()).toContain('U $0.00')
  })
})
