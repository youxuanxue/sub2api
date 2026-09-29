import { expect, test, type Page } from '@playwright/test'
import type { ImageGenerationCapability } from '../src/api/api-key-capabilities'

const gemini = 'gemini-3.1-flash-image'
const gpt = 'gpt-image-1'
const geminiProfile: ImageGenerationCapability = { endpoint: '/v1/chat/completions', aspect_ratios: ['1:1', '3:4', '4:3', '9:16', '16:9'], counts: [1], input_image: false, soft_aspect_ratio: false }
const gptProfile: ImageGenerationCapability = { endpoint: '/v1/images/generations', aspect_ratios: ['1:1', '3:2', '2:3', '16:9', '9:16'], counts: [1, 2], input_image: false, soft_aspect_ratio: true }
const inlineImage = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a9X8AAAAASUVORK5CYII='

async function fixture(page: Page, authenticated = true) {
  const user = { id: 7, username: 'image-regression', email: 'image@tokenkey.test', role: 'user', balance: 100, status: 'active', onboarding_tour_seen_at: '2026-09-18T00:00:00Z' }
  const calls: { path: string; method: string; body?: Record<string, unknown> }[] = []
  if (authenticated) await page.addInitScript(user => { localStorage.setItem('auth_token', 'fixture-session'); localStorage.setItem('auth_user', JSON.stringify(user)) }, user)
  await page.addInitScript(() => localStorage.setItem('tokenkey_locale', 'zh'))
  await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
  await page.route('**/api/v1/**', route => {
    const path = new URL(route.request().url()).pathname
    calls.push({ path, method: route.request().method() })
    const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })
    if (path.endsWith('/auth/me')) return ok(user)
    if (path.endsWith('/auth/refresh')) return route.fulfill({ status: 401, json: { message: 'no session' } })
    if (path.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', api_base_url: 'http://127.0.0.1:4196', custom_menu_items: [], custom_endpoints: [], registration_enabled: true, registration_offer: { state: 'open' } })
    if (path.endsWith('/keys')) return ok({ items: [42, 43].map(id => ({ id, name: id === 42 ? 'Universal images' : 'Restricted images', key: `sk-fixture-${id}`, status: 'active', routing_mode: 'universal', group_id: null })), total: 2 })
    if (path.endsWith('/capabilities')) {
      const restricted = path.includes('/43/')
      return ok({ models: [gemini, gpt].map(id => ({ id, protocols: ['openai', 'gemini'], modalities: ['image'], routes: [], image_generation: [id === gpt ? gptProfile : restricted ? { ...geminiProfile, aspect_ratios: ['1:1'] } : geminiProfile] })) })
    }
    if (path.endsWith('/me/pricing-catalog')) return ok({ models: [gemini, gpt].map(id => ({ model_id: id, billing_mode: 'image', your_price: { currency: 'USD', per_image: 0.04 } })) })
    if (path.endsWith('/public/pricing')) return route.fulfill({ json: { object: 'list', data: [] } })
    return ok([])
  })
  await page.route('**/v1/models', route => {
    calls.push({ path: '/v1/models', method: route.request().method() })
    return route.fulfill({ json: { object: 'list', data: [gemini, gpt].map(id => ({ id })) } })
  })
  for (const path of ['/v1/images/generations', '/v1/chat/completions']) await page.route(`**${path}`, route => {
    calls.push({ path, method: route.request().method(), body: route.request().postDataJSON() })
    return route.fulfill({ json: path.includes('/images/') ? { data: [{ b64_json: inlineImage }] } : { choices: [{ message: { content: `![image](data:image/png;base64,${inlineImage})` } }] } })
  })
  return calls
}

test('Studio model and key switches normalize ratios, and send the correct image payload', async ({ page }, info) => {
  const calls = await fixture(page)
  await page.goto(`/studio?mode=image&key=42&model=${gemini}&ratio=16%3A9`)
  const ratios = page.getByTestId('studio-image-aspect')
  await expect(ratios).toHaveCount(5)
  await expect(ratios.filter({ hasText: '16:9' })).toHaveAttribute('aria-pressed', 'true')
  expect(calls.filter(c => c.body)).toEqual([])
  await page.getByTestId('studio-image-generate').click()
  await expect(page.getByTestId('studio-image-thumb')).toHaveCount(1)
  expect(calls.find(c => c.body)?.body).toMatchObject({ model: gemini, extra_body: { google: { image_config: { aspect_ratio: '16:9' } } } })
  await page.getByTestId('studio-image-model').filter({ hasText: gpt }).click()
  await expect(page.getByTestId('image-exact-canvas')).toBeVisible()
  await expect(page.getByTestId('image-soft-ratio')).toHaveCount(0)
  await expect(ratios.filter({ hasText: '16:9' })).toHaveAttribute('aria-pressed', 'true')
  await page.getByTestId('studio-image-generate').click()
  await expect(page.getByTestId('studio-image-thumb')).toHaveCount(2)
  const request = calls.filter(c => c.path === '/v1/images/generations')[0].body!
  expect(request.size).toBe('2048x1152')
  expect(request).not.toHaveProperty('aspect_ratio')
  expect(request).not.toHaveProperty('max_tokens')
  await ratios.filter({ hasText: '3:2' }).click()
  await page.getByTestId('studio-image-thumb').last().click()
  await page.getByRole('button', { name: '用此 prompt', exact: true }).last().click()
  await expect(ratios.filter({ hasText: '16:9' })).toHaveAttribute('aria-pressed', 'true')
  await page.locator('select').first().selectOption('43')
  await expect(ratios).toHaveCount(1)
  await expect(ratios).toHaveAttribute('aria-pressed', 'true')
  await page.screenshot({ path: info.outputPath('studio-key-normalization.png'), fullPage: true })
})

test('Quickstart image examples verify only the key, then open Studio with matching controls', async ({ page }, info) => {
  const calls = await fixture(page)
  await page.goto(`/quickstart?client=curl&keyId=42&model=${gpt}`)
  await expect(page.getByTestId('quickstart-image-example')).toBeVisible()
  await page.getByTestId('image-generation-aspect').filter({ hasText: '16:9' }).click()
  const code = page.locator('pre code').first()
  await expect(code).toContainText('/v1/images/generations')
  await expect(code).toContainText('"size": "2048x1152"')
  await expect(code).not.toContainText('aspect_ratio')
  await expect(code).not.toContainText('max_tokens')
  await expect(page.locator('[data-tk="quickstart-send-test"]')).toHaveText('验证密钥')
  await page.locator('[data-tk="quickstart-send-test"]').click()
  await expect(page.locator('[data-tk="quickstart-connection-health"]')).toContainText('生图未验证')
  expect(calls.filter(c => c.body)).toEqual([])
  await page.screenshot({ path: info.outputPath('quickstart-gpt-image.png'), fullPage: true })
  await page.getByTestId('quickstart-open-studio').click()
  await expect(page).toHaveURL(/\/studio\?.*ratio=16%3A9/)
  await expect(page.getByTestId('studio-image-aspect').filter({ hasText: '16:9' })).toHaveAttribute('aria-pressed', 'true')
  expect(calls.filter(c => c.body)).toEqual([])
  await page.getByTestId('studio-image-aspect').filter({ hasText: '3:2' }).click()
  await page.getByRole('link', { name: '工具接入', exact: true }).click()
  await page.locator('[data-tk="quickstart-client-curl"]').click()
  await page.locator('[data-tk="use-key-model-select"]').selectOption(gemini)
  await page.getByTestId('image-generation-aspect').filter({ hasText: '4:3' }).click()
  await page.getByTestId('quickstart-open-studio').click()
  await expect(page.getByTestId('studio-image-aspect').filter({ hasText: '4:3' })).toHaveAttribute('aria-pressed', 'true')
  expect(calls.filter(c => c.body)).toEqual([])
  await page.goto(`/quickstart?client=python&keyId=42&model=${gemini}`)
  await expect(page.locator('pre code').first()).toContainText('/v1/chat/completions')
  await expect(page.locator('pre code').first()).toContainText('filename.write_bytes(data)')
  await expect(page.locator('pre code').first()).not.toContainText('max_tokens')
})

test('public image preview stays anonymous and preserves the Studio destination through login', async ({ page }, info) => {
  const calls = await fixture(page, false)
  await page.goto(`/quickstart?client=curl&model=${gpt}`)
  await expect(page.getByTestId('quickstart-image-example')).toBeVisible()
  await expect(page.locator('pre code').first()).toContainText('YOUR_TOKENKEY_API_KEY')
  await expect(page.locator('pre code').first()).toContainText('/v1/images/generations')
  expect(calls.filter(c => /\/keys$|\/capabilities$|^\/v1\//.test(c.path))).toEqual([])
  await page.screenshot({ path: info.outputPath('public-image-preview.png'), fullPage: true })
  await page.getByTestId('quickstart-open-studio').click()
  await expect(page).toHaveURL(/\/login\?redirect=/)
  const destination = new URL(page.url()).searchParams.get('redirect')!
  expect(destination).toContain(`/studio?mode=image&model=${gpt}`)
  expect(destination).not.toContain('sk-')
  expect(calls.filter(c => c.body)).toEqual([])
})

test('BakeOff shares Gemini and GPT image request defaults', async ({ page }) => {
  const calls = await fixture(page)
  await page.goto('/studio?mode=bakeoff&key=42')
  await page.getByTestId('bakeoff-mode-image').click()
  await expect(page.getByTestId('bakeoff-tier')).toHaveCount(2)
  for (const tile of await page.getByTestId('bakeoff-tier').all()) await tile.click()
  await page.locator('textarea').fill('Generate an image of a paper boat')
  await page.getByTestId('studio-bakeoff-run').click()
  await expect(page.getByTestId('bakeoff-panel')).toHaveCount(2)
  await expect.poll(() => calls.filter(c => c.body).length).toBe(2)
  expect(calls.find(c => c.path === '/v1/images/generations')?.body).toMatchObject({ model: gpt, size: '1024x1024' })
  expect(calls.find(c => c.path === '/v1/images/generations')?.body).not.toHaveProperty('aspect_ratio')
  expect(calls.find(c => c.path === '/v1/chat/completions')?.body).toMatchObject({ model: gemini, extra_body: { google: { image_config: { aspect_ratio: '1:1' } } } })
})
