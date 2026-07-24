import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

const BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:8089'
const LOGIN_EMAIL = process.env.E2E_LOGIN_EMAIL || 'admin@sub2api.local'
const LOGIN_PASSWORD = process.env.E2E_LOGIN_PASSWORD || 'Admin12345!'

async function loginSession(request: APIRequestContext) {
  const loginResp = await request.post(`${BASE}/api/v1/auth/login`, {
    data: { email: LOGIN_EMAIL, password: LOGIN_PASSWORD },
  })
  expect(loginResp.ok(), `login failed: ${loginResp.status()}`).toBeTruthy()
  const loginBody = await loginResp.json()
  expect(loginBody.code, `login API error: ${loginBody.message}`).toBe(0)

  const token = loginBody.data.access_token as string
  const meResp = await request.get(`${BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(meResp.ok(), `auth/me failed: ${meResp.status()}`).toBeTruthy()
  const meBody = await meResp.json()
  expect(meBody.code).toBe(0)

  return { token, user: meBody.data as Record<string, unknown> }
}

async function seedAuth(page: Page, token: string, user: Record<string, unknown>) {
  await page.setViewportSize({ width: 1280, height: 520 })
  const userWithTour = {
    ...user,
    onboarding_tour_seen_at:
      (user.onboarding_tour_seen_at as string | undefined) || new Date().toISOString(),
  }
  await page.addInitScript(
    ({ token, user }) => {
      localStorage.setItem('auth_token', token)
      localStorage.setItem('auth_user', JSON.stringify(user))
      const uid = typeof user.id === 'number' ? user.id : 0
      const role = typeof user.role === 'string' ? user.role : 'user'
      if (uid > 0) {
        const guideKey =
          role === 'admin'
            ? `admin_guide_${uid}_admin_v4_interactive`
            : `user_guide_${uid}_user_v4_interactive`
        localStorage.setItem(guideKey, 'true')
      }
    },
    { token, user: userWithTour },
  )
}

async function dismissOnboardingOverlay(page: Page): Promise<void> {
  const overlay = page.locator('.driver-overlay')
  if ((await overlay.count()) === 0) return
  await page.keyboard.press('Escape')
  await overlay.waitFor({ state: 'hidden', timeout: 10_000 }).catch(async () => {
    await page.evaluate(() => {
      document.querySelector('.driver-overlay')?.remove()
      document.querySelector('.driver-popover')?.remove()
    })
  })
}

async function sidebarScrollTop(page: Page): Promise<number> {
  return page.locator('nav.sidebar-nav').evaluate((el) => el.scrollTop)
}

async function scrollSidebarDown(page: Page): Promise<number> {
  const max = await page.locator('nav.sidebar-nav').evaluate((el) => Math.max(0, el.scrollHeight - el.clientHeight))
  expect(max, 'sidebar must be tall enough to scroll in this viewport').toBeGreaterThan(120)
  const delta = Math.min(max, 280)
  await page.locator('nav.sidebar-nav').evaluate((el, d) => {
    el.scrollTop = d
  }, delta)
  await page.waitForTimeout(150)
  return sidebarScrollTop(page)
}

async function clickSidebarLink(page: Page, href: string): Promise<void> {
  await dismissOnboardingOverlay(page)
  const link = page.locator(`nav.sidebar-nav a.sidebar-link[href="${href}"]`).first()
  await expect(link).toBeVisible()
  await link.click()
  await page.waitForURL((u) => u.pathname === href || u.pathname.startsWith(`${href}/`), { timeout: 20_000 })
  await page.waitForTimeout(300)
}

test.describe('sidebar scroll preservation (local deploy)', () => {
  test('keeps sidebar scroll across console routes against real backend', async ({ page, request }) => {
    const { token, user } = await loginSession(request)
    await seedAuth(page, token, user)

    await page.goto('/profile')
    await page.locator('nav.sidebar-nav').waitFor({ timeout: 30_000 })
    await dismissOnboardingOverlay(page)
    expect(await scrollSidebarDown(page)).toBeGreaterThan(100)

    for (const href of ['/usage', '/monitor', '/models', '/keys', '/studio', '/quickstart']) {
      await clickSidebarLink(page, href)
      expect(await sidebarScrollTop(page), `${href} must not jump back to top`).toBeGreaterThan(50)
    }
  })
})
