import { expect, test, type Page } from '@playwright/test'

const EMAIL = process.env.E2E_EMAIL || 'admin@tokenkey.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((url) => !url.pathname.includes('/login'), { timeout: 30_000 })
}

async function openSettingsTab(page: Page, tab: 'gateway' | 'payment'): Promise<void> {
  await page.locator(`#settings-tab-${tab}`).click()
  await expect(page.locator(`#settings-tab-${tab}`)).toHaveAttribute('aria-selected', 'true')
}

async function dismissOnboarding(page: Page): Promise<void> {
  const dialog = page.getByRole('dialog', { name: /Welcome|欢迎/ })
  if (await dialog.isVisible().catch(() => false)) {
    await dialog.getByRole('button', { name: /Close|关闭/ }).click()
  }
}

test.describe('PR #1468 upstream settings integration', () => {
  test('gateway keeps TokenKey controls while exposing upstream usage settings', async ({ page }) => {
    await login(page)
    await page.goto('/admin/settings')
    await dismissOnboarding(page)
    await openSettingsTab(page, 'gateway')

    await expect(page.getByTestId('upstream-billing-probe-settings')).toBeVisible()
    await expect(page.getByTestId('ollama-cloud-usage-global-settings')).toBeVisible()
    await expect(page.getByTestId('openai-low-rate-priority-toggle')).toBeVisible()
    await expect(page.getByTestId('openai-advanced-scheduler-toggle')).toBeVisible()

    await page.getByTestId('openai-advanced-scheduler-toggle').click()
    await expect(page.getByTestId('openai-low-rate-priority-toggle')).toBeHidden()
    await expect(page.getByTestId('openai-advanced-scheduler-weights')).toBeVisible()

    await page.screenshot({
      path: 'e2e/artifacts/pr1468-settings-gateway-desktop.png',
      fullPage: true,
    })
  })

  test('mobile settings keep gateway and Alipay handoff controls reachable', async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 })
    await login(page)
    await page.goto('/admin/settings')
    await dismissOnboarding(page)

    await openSettingsTab(page, 'gateway')
    await expect(page.getByTestId('upstream-billing-probe-settings')).toBeVisible()
    await expect(page.getByTestId('ollama-cloud-usage-global-settings')).toBeVisible()

    await openSettingsTab(page, 'payment')
    await page.getByTestId('payment-enabled-toggle').click()
    const alipayHandoff = page.getByTestId('payment-alipay-mobile-precreate-deep-link')
    await expect(alipayHandoff).toBeVisible()
    await page.evaluate(() => {
      document.documentElement.scrollLeft = 0
      document.body.scrollLeft = 0
      window.scrollTo(0, 0)
    })
    await expect.poll(() => page.evaluate(() => window.scrollX)).toBe(0)

    const mainBox = await page.locator('main').boundingBox()
    expect(mainBox).toBeTruthy()
    expect(mainBox!.x).toBeGreaterThanOrEqual(0)
    expect(mainBox!.x + mainBox!.width).toBeLessThanOrEqual(390)

    const overflow = await page.evaluate(() =>
      Array.from(document.querySelectorAll<HTMLElement>('main *'))
        .map((element) => ({
          tag: element.tagName.toLowerCase(),
          className: element.className,
          testId: element.dataset.testid,
          left: Math.round(element.getBoundingClientRect().left),
          right: Math.round(element.getBoundingClientRect().right),
          insideTabScroller: Boolean(element.closest('.settings-tabs-scroll')),
        }))
        .filter(({ right, insideTabScroller }) =>
          !insideTabScroller && right > window.innerWidth + 1,
        )
        .slice(0, 12),
    )
    expect(overflow).toEqual([])

    await alipayHandoff.scrollIntoViewIfNeeded()
    await expect(alipayHandoff).toBeInViewport()

    await page.screenshot({
      path: 'e2e/artifacts/pr1468-settings-payment-mobile.png',
    })
  })
})
