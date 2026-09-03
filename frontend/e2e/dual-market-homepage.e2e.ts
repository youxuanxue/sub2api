import { expect, test, type BrowserContext, type Locator, type Page, type Route } from '@playwright/test'

const port = process.env.E2E_HOMEPAGE_PORT || '3000'
const scheme = process.env.E2E_HOMEPAGE_SCHEME || 'http'
const currentHomepage = `${scheme}://tokenkey.dev:${port}/home`
const chinaExportHomepage = `${scheme}://global.tokenkey.dev:${port}/home`

const publicSettings = {
  api_base_url: 'https://api.tokenkey.dev',
  compact_home_enabled: false,
  custom_endpoints: [],
  custom_menu_items: [],
  home_content: '',
  model_plaza_enabled: false,
  pricing_catalog_public: true,
  registration_enabled: true,
  site_logo: '/logo.svg',
  site_name: 'Sub2API',
  site_subtitle: 'One key for every AI model.',
}

const testUser = {
  id: 1973,
  username: 'global-home-e2e',
  email: 'global-home-e2e@tokenkey.test',
  role: 'user',
  balance: 1,
  concurrency: 5,
  status: 'active',
  allowed_groups: null,
  balance_notify_enabled: false,
  balance_notify_threshold: null,
  balance_notify_extra_emails: [],
  onboarding_tour_seen_at: null,
  created_at: '2026-09-03T00:00:00Z',
  updated_at: '2026-09-03T00:00:00Z',
}

const defaultKey = {
  id: 1973,
  user_id: testUser.id,
  key: 'sk-tokenkey-global-home-e2e',
  name: 'Default Key',
  group_id: null,
  routing_mode: 'universal',
  status: 'active',
  ip_whitelist: [],
  ip_blacklist: [],
  last_used_at: null,
  last_used_ip: null,
  quota: 0,
  quota_used: 0,
  expires_at: null,
  created_at: '2026-09-03T00:00:00Z',
  updated_at: '2026-09-03T00:00:00Z',
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

async function fulfillOk(route: Route, data: unknown) {
  await route.fulfill({
    status: 200,
    contentType: 'application/json',
    json: ok(data),
  })
}

async function installPublicHomepageFixture(page: Page) {
  await page.route('**/api/v1/settings/public', (route) => route.fulfill({ json: ok(publicSettings) }))
  await page.route('**/api/v1/auth/refresh', (route) =>
    route.fulfill({
      status: 500,
      json: { code: 500, message: 'no session fixture' },
    }),
  )
  await page.route('**/setup/status', (route) => route.fulfill({ json: ok({ needs_setup: false }) }))
}

async function installAuthenticatedProductFixture(page: Page) {
  await page.route('**/api/v1/auth/me', (route) => fulfillOk(route, testUser))
  await page.route('**/api/v1/subscriptions/active', (route) => fulfillOk(route, []))
  await page.route('**/api/v1/announcements**', (route) => fulfillOk(route, []))
  await page.route('**/api/v1/keys?**', (route) =>
    fulfillOk(route, {
      items: [defaultKey],
      total: 1,
      page: 1,
      page_size: 100,
      pages: 1,
    }),
  )
  await page.route(`**/api/v1/me/api-keys/${defaultKey.id}/capabilities**`, (route) =>
    fulfillOk(route, {
      api_key_id: defaultKey.id,
      routing_mode: 'universal',
      models: [
        {
          id: 'deepseek-chat',
          protocols: ['openai', 'codex'],
          modalities: ['chat'],
          routes: [],
        },
      ],
    }),
  )
}

async function addParentDomainSession(context: BrowserContext) {
  await context.addCookies([
    {
      name: 'tk_refresh',
      value: 'parent-domain-session',
      domain: '.tokenkey.dev',
      path: '/',
      httpOnly: true,
      secure: scheme === 'https',
      sameSite: 'Lax',
    },
  ])
}

async function expectNoViewportOverflow(page: Page) {
  const metrics = await page.evaluate(() => ({
    clientWidth: document.documentElement.clientWidth,
    scrollWidth: document.documentElement.scrollWidth,
  }))
  expect(metrics.scrollWidth).toBeLessThanOrEqual(metrics.clientWidth)
}

function rectanglesOverlap(
  first: { x: number; y: number; width: number; height: number },
  second: { x: number; y: number; width: number; height: number },
) {
  return !(
    first.x + first.width <= second.x ||
    second.x + second.width <= first.x ||
    first.y + first.height <= second.y ||
    second.y + second.height <= first.y
  )
}

async function terminalVisualTokens(terminal: Locator) {
  return terminal.evaluate((node) => {
    const windowStyle = getComputedStyle(node)
    const headerStyle = getComputedStyle(node.querySelector('.terminal-header')!)
    const bodyStyle = getComputedStyle(node.querySelector('.terminal-body')!)

    return {
      window: [windowStyle.backgroundImage, windowStyle.borderRadius, windowStyle.boxShadow],
      header: [headerStyle.backgroundColor, headerStyle.borderBottomColor, headerStyle.padding],
      body: [bodyStyle.fontFamily, bodyStyle.fontSize, bodyStyle.lineHeight, bodyStyle.padding],
    }
  })
}

test.describe('dual-market homepage', () => {
  test.beforeEach(async ({ page }) => {
    await installPublicHomepageFixture(page)
  })

  test('keeps the current homepage unchanged on the apex host', async ({ page }) => {
    await page.setViewportSize({ width: 1280, height: 720 })
    await page.goto(currentHomepage)

    const heroTitle = page.locator('[data-home-profile="current"] h1')
    const heroSubtitle = page.locator('[data-home-profile="current"] h1 + p')
    const terminal = page.locator('.terminal-container')
    await expect(heroTitle).toBeVisible()
    await expect(heroSubtitle).toBeVisible()
    await expect(terminal).toBeVisible()
    await expect(page.locator('[data-testid="china-export-home"]')).toHaveCount(0)

    const [titleBox, subtitleBox, terminalBox] = await Promise.all([
      heroTitle.boundingBox(),
      heroSubtitle.boundingBox(),
      terminal.boundingBox(),
    ])
    expect(titleBox).not.toBeNull()
    expect(subtitleBox).not.toBeNull()
    expect(terminalBox).not.toBeNull()
    expect(rectanglesOverlap(titleBox!, terminalBox!)).toBe(false)
    expect(rectanglesOverlap(subtitleBox!, terminalBox!)).toBe(false)
    await expectNoViewportOverflow(page)
  })

  test('renders the China model story and real Seedance media on desktop', async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 })
    await page.goto(chinaExportHomepage)

    await expect(page.locator('h1')).toHaveText("China's leading AI models. One API.")
    await expect(page.locator('[data-testid="china-model-list"] h3')).toHaveText([
      'Seedance',
      'Seedream',
      'Qwen',
      'DeepSeek',
      'GLM',
      'Kimi',
    ])
    await expect(page.locator('[data-testid="china-export-primary-cta"]')).toHaveAttribute(
      'href',
      'https://tokenkey.dev/register?redirect=%2Fquickstart%3Fmodel%3Ddeepseek-chat%26protocol%3Dopenai',
    )
    await expect(page.locator('header')).toContainText('TokenKey')
    await expect(page.locator('header img')).toHaveAttribute('src', '/logo.png')
    await expect(page.locator('footer')).toContainText('TokenKey')
    await expect(page.locator('footer')).not.toContainText('Sub2API')
    await expect(page.locator('[data-testid="china-export-home"]')).toContainText('Free trial')
    await expect(page.locator('[data-testid="china-export-home"]')).not.toContainText('$1')
    await expect(page.locator('[data-testid="china-export-home"]')).not.toContainText('1M')

    const video = page.locator('[data-testid="seedance-proof-video"]')
    await expect(video).toBeVisible()
    await expect
      .poll(() => video.evaluate((node: HTMLVideoElement) => node.videoWidth * node.videoHeight))
      .toBeGreaterThan(0)
    await expectNoViewportOverflow(page)
  })

  test('switches the China export homepage between English and Chinese', async ({ page }) => {
    await page.addInitScript(() => {
      if (!localStorage.getItem('tokenkey_locale')) {
        localStorage.setItem('tokenkey_locale', 'en')
      }
    })
    await page.goto(chinaExportHomepage)

    await expect(page.locator('h1')).toHaveText("China's leading AI models. One API.")
    await page.locator('header button').filter({ hasText: 'EN' }).click()
    await page.getByRole('button', { name: /中文/ }).click()
    await expect(page.locator('h1')).toHaveText('中国领先 AI 模型，一个 API。')
    await expect(page.locator('[data-testid="china-export-home"]')).toContainText('免费试用')
    await expect(page.locator('html')).toHaveAttribute('lang', 'zh')

    await page.reload()
    await expect(page.locator('h1')).toHaveText('中国领先 AI 模型，一个 API。')
  })

  test('restores the parent-domain session on the global homepage', async ({ page, context }) => {
    await addParentDomainSession(context)
    await page.unroute('**/api/v1/auth/refresh')
    let refreshCookie = ''
    await page.route('**/api/v1/auth/refresh', async (route) => {
      refreshCookie = route.request().headers().cookie ?? ''
      await fulfillOk(route, {
        access_token: 'global-home-access',
        refresh_token: 'global-home-refresh',
        expires_in: 3600,
        token_type: 'Bearer',
      })
    })
    await page.route('**/api/v1/auth/me', (route) => fulfillOk(route, testUser))

    await page.goto(chinaExportHomepage)

    await expect.poll(() => refreshCookie).toContain('tk_refresh=parent-domain-session')
    await expect(page.getByRole('link', { name: /dashboard/i })).toHaveAttribute(
      'href',
      'https://tokenkey.dev/dashboard',
    )
  })

  test('drives the global CTA through registration into the DeepSeek quickstart', async ({ page }) => {
    await installAuthenticatedProductFixture(page)
    await page.route('**/api/v1/auth/register', (route) =>
      fulfillOk(route, {
        access_token: 'registered-global-home-access',
        refresh_token: 'registered-global-home-refresh',
        expires_in: 3600,
        token_type: 'Bearer',
        user: testUser,
      }),
    )
    await page.route('https://tokenkey.dev/register?**', (route) => route.abort())
    await page.goto(chinaExportHomepage)

    const navigationRequest = page.waitForRequest((request) => {
      const target = new URL(request.url())
      return request.isNavigationRequest() && target.hostname === 'tokenkey.dev' && target.pathname === '/register'
    })
    await page.locator('[data-testid="china-export-primary-cta"]').click({ noWaitAfter: true })
    const requestedRegistration = new URL((await navigationRequest).url())
    expect(requestedRegistration.searchParams.get('redirect')).toBe('/quickstart?model=deepseek-chat&protocol=openai')

    await page.goto(`${scheme}://tokenkey.dev:${port}${requestedRegistration.pathname}${requestedRegistration.search}`)
    await page.locator('#email').fill(testUser.email)
    await page.locator('#password').fill('TokenKey-E2E-Password-1973!')
    await page.locator('form button[type="submit"]').click()

    await page.waitForURL(
      (url) =>
        url.hostname === 'tokenkey.dev' &&
        url.pathname === '/quickstart' &&
        url.searchParams.get('model') === 'deepseek-chat' &&
        url.searchParams.get('protocol') === 'openai',
    )
    await expect(page.locator('[data-tk="use-key-model-select"]')).toHaveValue('deepseek-chat')
  })

  test('keeps terminal chrome and typography identical across both homepages', async ({ page }) => {
    await page.goto(currentHomepage)
    const currentTerminal = page.locator('[data-home-profile="current"] .terminal-window')
    await expect(currentTerminal).toBeVisible()
    const currentTokens = await terminalVisualTokens(currentTerminal)

    await page.goto(chinaExportHomepage)
    const exportTerminal = page.locator('[data-testid="china-export-terminal"] .terminal-window')
    await exportTerminal.scrollIntoViewIfNeeded()
    await expect(exportTerminal).toBeVisible()
    await expect(exportTerminal.locator('.terminal-title')).toHaveText('terminal')
    await expect(exportTerminal.locator('.terminal-buttons span')).toHaveCount(3)
    await expect(exportTerminal.locator('.terminal-copy-button')).toBeVisible()
    expect(await terminalVisualTokens(exportTerminal)).toEqual(currentTokens)
    await expectNoViewportOverflow(page)
  })

  test('fits the China model homepage on mobile without overlapping its primary actions', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await page.goto(chinaExportHomepage)

    const primary = page.locator('[data-testid="china-export-primary-cta"]')
    const models = page.getByRole('link', { name: 'Browse all models' })
    await expect(primary).toBeVisible()
    await expect(models).toBeVisible()

    const [primaryBox, modelsBox] = await Promise.all([primary.boundingBox(), models.boundingBox()])
    expect(primaryBox).not.toBeNull()
    expect(modelsBox).not.toBeNull()
    expect(rectanglesOverlap(primaryBox!, modelsBox!)).toBe(false)

    const nextSectionSignal = await page.locator('[data-testid="china-models-eyebrow"]').boundingBox()
    expect(nextSectionSignal).not.toBeNull()
    expect(nextSectionSignal!.y + nextSectionSignal!.height).toBeLessThanOrEqual(844)
    await expectNoViewportOverflow(page)
  })

  test('uses the poster instead of autoplay video for reduced motion', async ({ page }) => {
    await page.emulateMedia({ reducedMotion: 'reduce' })
    await page.goto(chinaExportHomepage)

    await expect(page.locator('[data-testid="seedance-proof-video"]')).toBeHidden()
    const poster = page.locator('[data-testid="seedance-proof-poster"]')
    await expect(poster).toBeVisible()
    await expect
      .poll(() => poster.evaluate((node: HTMLImageElement) => node.naturalWidth * node.naturalHeight))
      .toBeGreaterThan(0)
  })
})
