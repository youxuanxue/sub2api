import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

const API_BASE = process.env.E2E_API_BASE_URL || process.env.E2E_BASE_URL || 'http://127.0.0.1:3000'
const UI_BASE = process.env.E2E_BASE_URL || 'http://127.0.0.1:3000'
const LOGIN_EMAIL = process.env.E2E_LOGIN_EMAIL || 'admin@sub2api.local'
const LOGIN_PASSWORD = process.env.E2E_LOGIN_PASSWORD || 'Admin12345!'

async function loginSession(request: APIRequestContext) {
  const loginResp = await request.post(`${API_BASE}/api/v1/auth/login`, {
    data: { email: LOGIN_EMAIL, password: LOGIN_PASSWORD },
  })
  expect(loginResp.ok(), `login failed: ${loginResp.status()}`).toBeTruthy()
  const loginBody = await loginResp.json()
  expect(loginBody.code, `login API error: ${loginBody.message}`).toBe(0)

  const token = loginBody.data.access_token as string
  const meResp = await request.get(`${API_BASE}/api/v1/auth/me`, {
    headers: { Authorization: `Bearer ${token}` },
  })
  expect(meResp.ok(), `auth/me failed: ${meResp.status()}`).toBeTruthy()
  const meBody = await meResp.json()
  expect(meBody.code).toBe(0)

  return { token, user: meBody.data as Record<string, unknown> }
}

async function seedAuth(page: Page, token: string, user: Record<string, unknown>) {
  await page.setViewportSize({ width: 1360, height: 900 })
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

async function openQuickstart(page: Page): Promise<void> {
  await page.goto(`${UI_BASE}/quickstart`)
  await page.locator('[data-tk="quickstart-client-picker"]').waitFor({ timeout: 30_000 })
  await dismissOnboardingOverlay(page)
}

test.describe('Quickstart P0 tool-first flow', () => {
  test('tool picker leads, key lives in advanced options, connection health is primary CTA', async ({ page, request }) => {
    const { token, user } = await loginSession(request)
    await seedAuth(page, token, user)
    await openQuickstart(page)

    const picker = page.locator('[data-tk="quickstart-client-picker"]')
    const workspace = page.locator('[data-tk="quickstart-config-workspace"]')
    await expect(picker).toBeVisible()
    await expect(workspace).toBeVisible()

    const pickerBox = await picker.boundingBox()
    const workspaceBox = await workspace.boundingBox()
    expect(pickerBox).toBeTruthy()
    expect(workspaceBox).toBeTruthy()
    expect(pickerBox!.y).toBeLessThan(workspaceBox!.y)

    await expect(page.locator('[data-tk="quickstart-key-select"]')).toBeHidden()

    const health = page.locator('[data-tk="quickstart-connection-health"]')
    await expect(health).toBeVisible()
    await expect(health).toContainText(/等待首次请求|Waiting for first request/)
    await expect(page.locator('[data-tk="quickstart-send-test"]')).toBeVisible()

    const legend = page.locator('[data-tk="quickstart-support-legend"]')
    await expect(legend).toBeVisible()
    await expect(legend).not.toHaveAttribute('open', '')

    const claude = page.locator('[data-tk="quickstart-client-claude-code"]')
    await expect(claude).toContainText(/推荐|Recommended/)
    await expect(claude).toHaveAttribute('aria-pressed', 'true')
  })

  test('switching tools updates selection and keeps key picker in advanced options', async ({ page, request }) => {
    const { token, user } = await loginSession(request)
    await seedAuth(page, token, user)
    await openQuickstart(page)

    await page.locator('[data-tk="quickstart-client-codex-cli"]').click()
    await expect(page.locator('[data-tk="quickstart-client-codex-cli"]')).toHaveAttribute('aria-pressed', 'true')
    await expect(page.locator('[data-tk="quickstart-client-claude-code"]')).toHaveAttribute('aria-pressed', 'false')
    await expect(page.locator('h2', { hasText: 'Codex CLI' })).toBeVisible()

    await page.locator('[data-tk="quickstart-advanced-options"] summary').click()
    await expect(page.locator('[data-tk="quickstart-key-select"]')).toBeVisible()
    const selectedKey = await page.locator('[data-tk="quickstart-key-select"]').inputValue()
    expect(selectedKey.length).toBeGreaterThan(0)
  })

  test('send test request drives connection health out of idle', async ({ page, request }) => {
    const { token, user } = await loginSession(request)
    await seedAuth(page, token, user)
    await openQuickstart(page)

    const sendTest = page.locator('[data-tk="quickstart-send-test"]')
    await expect(sendTest).toBeEnabled()
    await sendTest.click()

    const health = page.locator('[data-tk="quickstart-connection-health"]')
    await expect(health).toContainText(
      /正在验证|Testing connection|已连接|Connected|连接失败|Connection failed/,
      { timeout: 60_000 },
    )

    const healthText = await health.innerText()
    expect(healthText).not.toMatch(/等待首次请求|Waiting for first request/)
  })
})
