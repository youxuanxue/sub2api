import { expect, test, type Locator, type Page } from '@playwright/test'

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

async function installPublicHomepageFixture(page: Page) {
  const ok = (data: unknown) => ({ code: 0, message: 'ok', data })
  await page.route('**/api/v1/settings/public', (route) =>
    route.fulfill({ json: ok(publicSettings) }),
  )
  await page.route('**/api/v1/auth/refresh', (route) =>
    route.fulfill({ status: 500, json: { code: 500, message: 'no session fixture' } }),
  )
  await page.route('**/setup/status', (route) =>
    route.fulfill({ json: ok({ needs_setup: false }) }),
  )
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

    const nextSectionSignal = await page
      .locator('[data-testid="china-models-eyebrow"]')
      .boundingBox()
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
