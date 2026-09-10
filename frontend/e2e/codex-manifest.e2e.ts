import { readFile } from 'node:fs/promises'
import { expect, test } from '@playwright/test'

for (const width of [1440, 390]) {
  test(`Codex manifest download follows the selected key at ${width}px`, async ({ page, baseURL }) => {
    await page.setViewportSize({ width, height: 900 })
    const user = { id: 1, role: 'user', email: 'manifest@tokenkey.test', balance: 10, status: 'active', onboarding_tour_seen_at: '2026-09-09T00:00:00Z' }
    await page.addInitScript(value => {
      localStorage.setItem('auth_token', 'manifest-ui-test')
      localStorage.setItem('auth_user', JSON.stringify(value))
      localStorage.setItem('tokenkey_locale', 'en')
    }, user)
    const keys = [1, 2].map(id => ({ id, user_id: 1, name: `Key ${id}`, key: `sk-manifest-test-${id}`, routing_mode: 'universal', group_id: null, status: 'active' }))
    const unexpected: string[] = []
    const pageErrors: string[] = []
    page.on('pageerror', error => pageErrors.push(error.message))
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown
      if (path === '/api/v1/auth/me') data = user
      else if (path === '/api/v1/settings/public') data = { site_name: 'TokenKey', api_base_url: baseURL, custom_menu_items: [] }
      else if (path === '/api/v1/keys') data = { items: keys, total: 2, page: 1, page_size: 100, pages: 1 }
      else if (/\/me\/api-keys\/\d+\/capabilities$/.test(path)) data = { models: [{ id: 'gpt-5.6-sol', protocols: ['openai', 'codex'], modalities: ['chat'], routes: [], selected_group: { id: 1, name: 'OpenAI', platform: 'openai' } }] }
      else if (['/api/v1/subscriptions/active', '/api/v1/announcements'].includes(path)) data = []
      else {
        unexpected.push(path)
        await route.fulfill({ status: 404, json: { code: 404, message: 'Unexpected test request' } })
        return
      }
      await route.fulfill({ json: { code: 0, message: 'success', data } })
    })
    let fail = true
    const auth: string[] = []
    const manifest = { models: [{ slug: 'gpt-5.6-sol', display_name: 'GPT-5.6', supported_reasoning_levels: [{ effort: 'medium', description: 'Medium' }], default_reasoning_level: 'medium' }] }
    await page.route('**/v1/models?client_version=*', async route => {
      auth.push(route.request().headers().authorization)
      await route.fulfill({ status: fail ? 503 : 200, json: fail ? { error: 'unavailable' } : manifest })
      fail = false
    })
    await page.goto('/quickstart?client=codex-cli&keyId=1')
    const catalog = page.getByTestId('codex-model-catalog')
    const fetchButton = page.getByTestId('codex-model-catalog-fetch')
    await fetchButton.click()
    await expect(catalog.getByRole('alert')).toBeVisible()
    await fetchButton.click()
    const downloadButton = catalog.getByRole('button', { name: /download/i })
    await expect(downloadButton).toBeVisible()
    const downloading = page.waitForEvent('download')
    await downloadButton.click()
    const download = await downloading
    expect(download.suggestedFilename()).toBe('codex-models.json')
    expect(JSON.parse(await readFile((await download.path())!, 'utf8'))).toEqual(manifest)
    await page.locator('[data-tk="quickstart-advanced-options"] summary').click()
    await page.locator('#quickstart-key').selectOption('2')
    await expect(downloadButton).toHaveCount(0)
    await fetchButton.click()
    await expect(downloadButton).toBeVisible()
    expect(auth).toEqual(['Bearer sk-manifest-test-1', 'Bearer sk-manifest-test-1', 'Bearer sk-manifest-test-2'])
    expect(pageErrors).toEqual([])
    expect(unexpected).toEqual([])
    await catalog.scrollIntoViewIfNeeded()
    await page.screenshot({ path: test.info().outputPath('codex-manifest.png') })
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  })
}
