import { expect, test, type Page } from '@playwright/test'

// Real Vue UI with deterministic API boundaries. These are browser journey
// tests, not evidence of SMTP delivery, atomic provisioning or paid upstreams.
const user = { id: 1449, email: 'reader@example.test', username: 'Reader', role: 'user', status: 'active', balance: 1, concurrency: 5, onboarding_tour_seen_at: '2026-09-11T00:00:00Z' }
const key = { id: 1449, user_id: user.id, name: 'Quick Start', key: 'sk-fixture-only-quickstart', routing_mode: 'universal', group_id: null, status: 'active', quota: 0, quota_used: 0 }

async function fixture(page: Page, state = 'open', backendMode = false, emailVerification = false) {
  let signedIn = false
  let created = false
  let failSettings = false
  const calls: string[] = []
  const settings = {
    registration_enabled: state === 'open', registration_offer: { state, signup_bonus_usd: state === 'open' ? '1.00' : undefined },
    email_verify_enabled: emailVerification, turnstile_enabled: false, invitation_code_enabled: state === 'invitation_required',
    promo_code_enabled: false, affiliate_enabled: false, login_agreement_enabled: false,
    backend_mode_enabled: backendMode, api_base_url: 'http://127.0.0.1:4196', site_name: 'TokenKey',
    pricing_catalog_public: true, custom_menu_items: [], custom_endpoints: [],
  }
  await page.addInitScript(() => { localStorage.setItem('tokenkey_locale', 'zh') })
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', async route => {
    const path = new URL(route.request().url()).pathname
    calls.push(`${route.request().method()} ${path}`)
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, message: 'ok', data } })
    if (path.endsWith('/settings/public')) return failSettings ? route.fulfill({ status: 503, json: { message: 'offline' } }) : ok(settings)
    if (path.endsWith('/auth/refresh')) return route.fulfill({ status: 401, json: { message: 'no fixture session' } })
    if (path.endsWith('/auth/register') || path.endsWith('/auth/login')) {
      signedIn = true
      return ok({ access_token: 'fixture-session-only', token_type: 'Bearer', user })
    }
    if (path.endsWith('/auth/send-verify-code')) return ok({ countdown: 60 })
    if (path.endsWith('/auth/me')) return ok(user)
    if (path.endsWith('/keys')) {
      if (!signedIn) return route.fulfill({ status: 403, json: { message: 'anonymous keys forbidden' } })
      if (route.request().method() === 'POST') { created = true; return ok(key) }
      return ok({ items: created ? [key] : [], total: created ? 1 : 0, page: 1, page_size: 100, pages: 1 })
    }
    if (path.endsWith('/capabilities')) return ok({ api_key_id: key.id, routing_mode: 'universal', models: [
      { id: 'fixture-model', protocols: ['anthropic', 'openai', 'codex'], modalities: ['chat'], routes: [] },
    ] })
    if (path.endsWith('/announcements')) return ok({ items: [], total: 0 })
    return ok([])
  })
  await page.route('**/v1/models', route => { calls.push('gateway models'); return route.fulfill({ json: { object: 'list', data: [{ id: 'fixture-model' }] } }) })
  await page.route('**/v1/messages', route => { calls.push('gateway test'); return route.fulfill({ json: { content: [{ type: 'text', text: 'Hello' }], usage: { input_tokens: 1, output_tokens: 1 } } }) })
  await page.route('**/v1/chat/completions', route => {
    calls.push('gateway test')
    expect(route.request().headers().authorization).toBe(`Bearer ${key.key}`)
    const request = route.request().postDataJSON()
    expect(request.model).toBe('fixture-model')
    return route.fulfill({ json: { choices: [{ message: { role: 'assistant', content: 'Hello',
      tool_calls: [{ id: 'fixture-call', type: 'function', function: { name: 'tokenkey_quickstart_probe', arguments: '{"value":"ok"}' } }],
    }, finish_reason: 'stop' }], usage: { prompt_tokens: 1, completion_tokens: 1 } } })
  })
  return { calls, failSettings: () => { failSettings = true }, requireEmailVerification: () => { settings.email_verify_enabled = true } }
}

async function chooseQwen(page: Page) {
  await page.goto('/quickstart')
  await page.locator('[data-tk="quickstart-client-qwen-code"]').click()
  await page.locator('[data-tk="quickstart-protocol-openai"]').click()
  await expect(page.locator('[data-tk="quickstart-preview"]')).toBeVisible()
}

for (const verification of [false, true]) {
  test(`anonymous setup survives ${verification ? 'verified email' : 'direct'} registration and explicit key creation`, async ({ page }) => {
    const { calls } = await fixture(page, 'open', false, verification)
    await chooseQwen(page)
    expect(calls.filter(call => call.includes('/keys') || call.includes('/capabilities') || call.includes('gateway'))).toEqual([])
    await expect(page.locator('[data-tk="quickstart-ccs-import"]')).toHaveCount(0)
    await expect(page.locator('[data-tk="quickstart-config-workspace"]')).toContainText('YOUR_TOKENKEY_API_KEY')
    await page.getByRole('link', { name: '注册并测试', exact: true }).click()
    await page.locator('#email').fill('reader@example.test')
    await page.locator('#password').fill('TestPassword1449!')
    await page.locator('button[type="submit"]').click()
    if (verification) {
      await expect(page).toHaveURL(/email-verify/)
      await page.locator('#code').fill('123456')
      await page.locator('button[type="submit"]').click()
    }
    await expect(page).toHaveURL(/quickstart.*client=qwen-code/)
    await expect(page.locator('[data-tk="quickstart-protocol-openai"]')).toHaveAttribute('aria-pressed', 'true')
    expect(calls.filter(call => call.startsWith('POST') && call.endsWith('/keys'))).toHaveLength(0)
    await page.getByRole('button', { name: '创建 API Key', exact: true }).click()
    await expect(page.locator('[data-tk="quickstart-key-select"]')).toHaveValue(String(key.id))
    await expect(page.locator('[data-tk="quickstart-preview"]')).toHaveCount(0)
    expect(calls.filter(call => call.startsWith('POST') && call.endsWith('/keys'))).toHaveLength(1)
    expect(calls).not.toContain('gateway test')
    expect(page.url()).not.toContain(key.key)
    await page.locator('[data-tk="quickstart-send-test"]').click()
    await expect(page.locator('[data-tk="quickstart-connection-health"]')).toContainText('200')
    expect(calls.filter(call => call === 'gateway test')).toHaveLength(1)
  })
}

test('login returns to the chosen client without automatically testing', async ({ page }) => {
  const { calls } = await fixture(page)
  await chooseQwen(page)
  await page.getByRole('link', { name: '已有账号？登录', exact: true }).click()
  await page.locator('#email').fill('reader@example.test')
  await page.locator('#password').fill('TestPassword1449!')
  await page.locator('button[type="submit"]').click()
  await expect(page).toHaveURL(/quickstart.*client=qwen-code/)
  await expect(page.getByRole('button', { name: '创建 API Key', exact: true })).toBeVisible()
  expect(calls).not.toContain('gateway test')
})

test('closed and failed policy keep the guide readable on mobile in backend mode', async ({ page }) => {
  const state = await fixture(page, 'closed', true)
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/quickstart?client=codex-cli')
  await expect(page.getByRole('link', { name: '登录并测试', exact: true })).toBeVisible()
  await expect(page.getByText('邮箱注册暂未开放。', { exact: true })).toBeVisible()
  state.failSettings()
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await expect(page.getByText('暂时无法确认注册状态。', { exact: true })).toBeVisible()
  await expect(page.locator('[data-tk="quickstart-config-workspace"]')).toContainText('YOUR_TOKENKEY_API_KEY')
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true)
  await page.screenshot({ path: '/tmp/1449-public-quickstart-mobile.png', fullPage: true })
  await page.goto('/keys')
  await expect(page).toHaveURL(/login/)
})

test('invitation state and stale open offer never advertise an unconditional trial', async ({ page }) => {
  const state = await fixture(page, 'invitation_required')
  await page.goto('/quickstart')
  await expect(page.getByRole('link', { name: '使用邀请码注册', exact: true })).toBeVisible()
  state.failSettings()
  await page.evaluate(() => document.dispatchEvent(new Event('visibilitychange')))
  await expect(page.getByRole('link', { name: '登录并测试', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: '使用邀请码注册', exact: true })).toHaveCount(0)
})


test('registration rechecks changed email requirements before submitting', async ({ page }) => {
  const state = await fixture(page)
  await chooseQwen(page)
  await page.getByRole('link', { name: '注册并测试', exact: true }).click()
  await page.locator('#email').fill('reader@example.test')
  await page.locator('#password').fill('TestPassword1449!')
  state.requireEmailVerification()
  await page.locator('button[type="submit"]').click()
  await expect(page).toHaveURL(/email-verify/)
  expect(state.calls).not.toContain('POST /api/v1/auth/register')
  await page.locator('#code').fill('123456')
  await page.locator('button[type="submit"]').click()
  await expect(page).toHaveURL(/quickstart.*client=qwen-code/)
})
