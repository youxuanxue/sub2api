import { test, expect, type Page } from '@playwright/test'

const EMAIL = process.env.E2E_EMAIL || 'admin@sub2api.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

type PublicModel = {
  model_id: string
  pricing?: { billing_mode?: string }
}

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 30_000 })
}

async function installQuickstartFixture(page: Page): Promise<void> {
  const user = {
    id: 7,
    username: 'quickstart-e2e',
    email: 'quickstart-e2e@tokenkey.test',
    role: 'user',
    balance: 100,
    concurrency: 5,
    status: 'active',
    allowed_groups: null,
    balance_notify_enabled: false,
    balance_notify_threshold: null,
    balance_notify_extra_emails: [],
    onboarding_tour_seen_at: '2026-07-16T00:00:00Z',
    created_at: '2026-07-16T00:00:00Z',
    updated_at: '2026-07-16T00:00:00Z',
  }
  const universalKey = {
    id: 42,
    user_id: user.id,
    key: 'sk-tokenkey-quickstart-e2e',
    name: 'Quickstart E2E',
    group_id: null,
    routing_mode: 'universal',
    status: 'active',
    ip_whitelist: [],
    ip_blacklist: [],
    last_used_at: null,
    quota: 0,
    quota_used: 0,
    expires_at: null,
    created_at: '2026-07-16T00:00:00Z',
    updated_at: '2026-07-16T00:00:00Z',
    current_concurrency: 0,
    rate_limit_5h: 0,
    rate_limit_1d: 0,
    rate_limit_7d: 0,
    usage_5h: 0,
    usage_1d: 0,
    usage_7d: 0,
    window_5h_start: null,
    window_1d_start: null,
    window_7d_start: null,
    reset_5h_at: null,
    reset_1d_at: null,
    reset_7d_at: null,
  }
  const ok = (data: unknown) => ({ code: 0, message: 'ok', data })

  await page.addInitScript(({ token, persistedUser }) => {
    localStorage.setItem('auth_token', token)
    localStorage.setItem('auth_user', JSON.stringify(persistedUser))
    localStorage.setItem('theme', 'light')
  }, { token: 'quickstart-e2e-token', persistedUser: user })

  await page.route('**/api/v1/auth/me', (route) => route.fulfill({ json: ok(user) }))
  await page.route('**/api/v1/keys?**', (route) => route.fulfill({
    json: ok({ items: [universalKey], total: 1, page: 1, page_size: 100, pages: 1 }),
  }))
  await page.route('**/api/v1/me/pricing-catalog**', (route) => route.fulfill({
    json: ok({
      target_group: {
        id: 1,
        name: 'Universal',
        platform: 'openai',
        rate_multiplier: 1,
        list_multiplier: 1,
        has_override: false,
        is_exclusive: false,
        subscription_type: 'standard',
      },
      models: [
        {
          model_id: 'gpt-5.5',
          billing_mode: 'token',
          your_price: { currency: 'USD' },
          capabilities: ['tools'],
        },
        {
          model_id: 'claude-opus-4-8',
          billing_mode: 'token',
          your_price: { currency: 'USD' },
          capabilities: ['tools'],
        },
      ],
      my_keys: [],
      accessible_groups: [],
      authorized_groups_by_model: {
        'gpt-5.5': [{ id: 1, name: 'Universal OpenAI', platform: 'openai' }],
        'claude-opus-4-8': [{ id: 2, name: 'Universal Anthropic', platform: 'anthropic' }],
      },
      updated_at: '2026-07-16T00:00:00Z',
    }),
  }))
  await page.route('**/api/v1/public/pricing**', (route) => route.fulfill({
    json: {
      object: 'list',
      data: [
        {
          model_id: 'gpt-5.5',
          pricing: { currency: 'USD', input_per_1k_tokens: 0, output_per_1k_tokens: 0 },
          capabilities: ['tools'],
          context_window: 1_050_000,
          max_output_tokens: 128_000,
        },
        {
          model_id: 'claude-opus-4-8',
          pricing: { currency: 'USD', input_per_1k_tokens: 0, output_per_1k_tokens: 0 },
          capabilities: ['tools'],
          context_window: 1_000_000,
          max_output_tokens: 128_000,
        },
      ],
      updated_at: '2026-07-16T00:00:00Z',
    },
  }))
}

async function publicCatalogCounts(baseURL: string): Promise<{ all: number; image: number; video: number }> {
  const res = await fetch(`${baseURL}/api/v1/public/pricing`)
  if (!res.ok) return { all: 0, image: 0, video: 0 }
  const body = (await res.json()) as { data?: PublicModel[] }
  const models = body.data ?? []
  return {
    all: models.length,
    image: models.filter((m) => m.pricing?.billing_mode === 'image').length,
    video: models.filter((m) => m.pricing?.billing_mode === 'video').length,
  }
}

test.describe('catalog UX — models / pricing / quickstart', () => {
  test('model marketplace media tabs match public billing_mode inventory', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await page.goto('/models')
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible()

    const grid = page.locator('[data-tk="models-marketplace-grid"]')
    await expect(grid).toBeVisible({ timeout: 20_000 })
    const allCards = grid.locator('[data-tk^="models-marketplace-card-"]')
    await expect(allCards.first()).toBeVisible()

    if (counts.image > 0) {
      await page.locator('[data-tk="models-marketplace-tab-image"]').click()
      await expect(page.locator('[data-tk="models-marketplace-empty"]')).toHaveCount(0)
      await expect(page.locator('[data-tk^="models-marketplace-card-"]').first()).toBeVisible()
    }

    if (counts.video > 0) {
      await page.locator('[data-tk="models-marketplace-tab-video"]').click()
      await expect(page.locator('[data-tk="models-marketplace-empty"]')).toHaveCount(0)
      await expect(page.locator('[data-tk^="models-marketplace-card-"]').first()).toBeVisible()
    }
  })

  test('pricing authorized-groups quick start lands on quickstart with model prefilled', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await login(page)

    const res = await fetch(`${baseURL}/api/v1/public/pricing`, {
      headers: { Authorization: `Bearer ${await page.evaluate(() => localStorage.getItem('auth_token'))}` },
    })
    const body = (await res.json()) as { data?: PublicModel[] }
    const sample = body.data?.find((m) => m.model_id) ?? body.data?.[0]
    test.skip(!sample?.model_id, 'no sample model in catalog')

    const modelId = sample.model_id
    await page.goto(`/pricing?model=${encodeURIComponent(modelId)}`)
    await expect(page.locator('#pricing-model-search')).toHaveValue(modelId, { timeout: 20_000 })

    const quickstart = page.locator('[data-tk="pricing-quickstart-for-model"]').first()
    await expect(quickstart).toBeVisible({ timeout: 20_000 })
    await quickstart.click()

    await page.waitForURL((u) => u.pathname === '/quickstart' && u.searchParams.get('model') === modelId, {
      timeout: 20_000,
    })

    await expect(page.locator('[data-tk="use-key-models-empty"]')).toHaveCount(0)
    await expect(page.locator('[data-tk="use-key-model-select"]')).toHaveValue(modelId)
  })

  test('quickstart universal key does not show empty servable-models warning', async ({ page, baseURL }) => {
    const counts = await publicCatalogCounts(baseURL!)
    test.skip(counts.all === 0, 'public catalog empty in this environment')

    await login(page)
    await page.goto('/quickstart')
    await expect(page.locator('[data-tk="use-key-model-select"]')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-tk="use-key-models-empty"]')).toHaveCount(0)
  })

  test('quickstart tool-first picker drives client config across desktop and mobile', async ({ page }) => {
    await installQuickstartFixture(page)
    await page.addInitScript(() => {
      const openedUrls: string[] = []
      Object.defineProperty(window, '__tkOpenedUrls', { value: openedUrls, configurable: true })
      window.open = ((url?: string | URL) => {
        openedUrls.push(String(url ?? ''))
        return { opener: null } as Window
      }) as typeof window.open
    })
    await page.goto('/quickstart')

    const picker = page.locator('[data-tk="quickstart-client-picker"]')
    await expect(picker).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('[data-tk="quickstart-client-group-coding"]')).toBeVisible()
    await expect(page.locator('[data-tk="quickstart-client-group-apps"]')).toBeVisible()
    await expect(page.locator('[data-tk="quickstart-client-group-build"]')).toBeVisible()

    const expectedClients = [
      'claude-code',
      'codex-cli',
      'qwen-code',
      'gemini-cli',
      'opencode',
      'cline',
      'roo-code',
      'cherry-studio',
      'lobe-chat',
      'chatbox',
      'dify',
      'curl',
      'python',
    ]
    for (const id of expectedClients) {
      const client = page.locator(`[data-tk="quickstart-client-${id}"]`)
      await expect(client).toBeVisible()
      const name = client.locator('span').first()
      await expect(name).not.toHaveCSS('text-overflow', 'ellipsis')
      expect(await name.evaluate((element) => element.scrollWidth <= element.clientWidth)).toBe(true)
    }

    const qwen = page.locator('[data-tk="quickstart-client-qwen-code"]')
    await expect(qwen).toHaveAttribute('data-support-tier', 'compatible')
    await qwen.focus()
    await page.keyboard.press('Enter')
    await expect(qwen).toHaveAttribute('aria-pressed', 'true')

    const workspace = page.locator('[data-tk="quickstart-config-workspace"]')
    await expect(workspace).toBeVisible()
    await expect(workspace.getByRole('heading', { name: 'Qwen Code' })).toBeVisible()
    await expect(page.locator('[data-tk="quickstart-protocol-picker"]')).toBeVisible()
    await expect(workspace.getByRole('navigation', { name: 'Operating system' })).toBeVisible()
    await expect(workspace.getByRole('button', { name: 'macOS / Linux' })).toBeVisible()

    const qwenOpenAI = page.locator('[data-tk="quickstart-protocol-openai"]')
    await qwenOpenAI.click()
    await expect(qwenOpenAI).toHaveAttribute('aria-pressed', 'true')
    await expect(workspace.locator('pre code').last()).toContainText('"openai"')

    const codex = page.locator('[data-tk="quickstart-client-codex-cli"]')
    await codex.click()
    await expect(codex).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-tk="quickstart-transport-picker"]')).toBeVisible()
    const websocket = page.locator('[data-tk="quickstart-transport-websocket"]')
    await websocket.click()
    await expect(websocket).toHaveAttribute('aria-pressed', 'true')
    await expect(workspace.locator('pre code').first()).toContainText('supports_websockets = true')

    const curl = page.locator('[data-tk="quickstart-client-curl"]')
    await curl.click()
    await expect(curl).toHaveAttribute('aria-pressed', 'true')
    await expect(workspace.getByRole('navigation', { name: 'Operating system' })).toHaveCount(0)
    await expect(workspace.locator('pre code').first()).toContainText('/v1/chat/completions')

    const opencode = page.locator('[data-tk="quickstart-client-opencode"]')
    await opencode.click()
    await expect(opencode).toHaveAttribute('aria-pressed', 'true')
    const openCodeConfig = JSON.parse(await workspace.locator('pre code').first().innerText())
    expect(openCodeConfig.provider.openai.options).toEqual({
      baseURL: `${new URL(page.url()).origin}/v1`,
      apiKey: 'sk-tokenkey-quickstart-e2e',
    })
    expect(Object.keys(openCodeConfig.provider.openai.models)).toEqual(['gpt-5.5'])
    const preview = workspace.locator('[data-tk="quickstart-config-preview-0"]')
    await expect(preview).toBeVisible()
    expect((await preview.boundingBox())?.height).toBeLessThanOrEqual(321)
    await workspace.locator('[data-tk="quickstart-config-toggle-0"]').click()
    expect((await preview.boundingBox())?.height).toBeGreaterThan(321)
    await workspace.locator('[data-tk="quickstart-config-toggle-0"]').click()

    await page.evaluate(() => window.scrollTo(0, 0))
    await page.screenshot({ path: 'e2e/artifacts/quickstart-desktop.png', fullPage: true })

    await page.locator('[data-tk="quickstart-client-cherry-studio"]').click()
    await page.locator('[data-tk="quickstart-client-import"]').click()
    await page.locator('[data-tk="quickstart-client-chatbox"]').click()
    await page.locator('[data-tk="quickstart-client-import"]').click()
    const openedUrls = await page.evaluate(() =>
      (window as Window & { __tkOpenedUrls?: string[] }).__tkOpenedUrls ?? [],
    )
    expect(openedUrls).toHaveLength(2)
    const cherryPayload = JSON.parse(atob(new URL(openedUrls[0]).searchParams.get('data')!))
    expect(cherryPayload).toMatchObject({
      id: 'tokenkey',
      baseUrl: `${new URL(page.url()).origin}/v1`,
      apiKey: 'sk-tokenkey-quickstart-e2e',
    })
    const chatboxPayload = JSON.parse(atob(new URL(openedUrls[1]).searchParams.get('config')!))
    expect(chatboxPayload.settings).toMatchObject({
      apiKey: 'sk-tokenkey-quickstart-e2e',
      models: [{ modelId: 'gpt-5.5' }],
    })
    expect(page.url()).not.toContain('sk-tokenkey-quickstart-e2e')

    await page.setViewportSize({ width: 390, height: 844 })
    await page.locator('[data-tk="quickstart-client-cherry-studio"]').scrollIntoViewIfNeeded()
    await expect(page.locator('[data-tk="quickstart-client-cherry-studio"]')).toBeVisible()
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth))
      .toBe(true)

    await qwen.scrollIntoViewIfNeeded()
    await qwen.focus()
    await page.keyboard.press('Enter')
    await expect(qwen).toHaveAttribute('aria-pressed', 'true')
    await workspace.evaluate((element) => element.scrollIntoView({ block: 'start' }))
    await expect(workspace).toBeVisible()
    await page.screenshot({ path: 'e2e/artifacts/quickstart-mobile.png', fullPage: true })
  })
})
