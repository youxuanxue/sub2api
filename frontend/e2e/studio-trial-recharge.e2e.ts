import { expect, test } from '@playwright/test'

for (const mode of ['image', 'video', 'bakeoff'] as const) {
  test(`trial media denial offers recharge in ${mode} Studio`, async ({ page }, info) => {
    const user = { id: 7, username: 'trial', email: 'trial@example.test', role: 'user', balance: 1, status: 'active', onboarding_tour_seen_at: '2026-09-18T00:00:00Z' }
    const models = mode === 'video' ? ['grok-imagine-video-1.5'] : mode === 'bakeoff' ? ['gpt-image-1', 'gpt-image-1.5'] : ['gpt-image-1']
    const kind = mode === 'video' ? 'video' : 'image'
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-session')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'zh')
    }, user)
    const ok = (data: unknown) => ({ code: 0, data })
    await page.route('**/setup/status', route => route.fulfill({ json: ok({ needs_setup: false }) }))
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = []
      if (path.endsWith('/auth/me')) data = user
      if (path.endsWith('/settings/public')) data = { api_base_url: new URL(route.request().url()).origin, custom_menu_items: [], custom_endpoints: [] }
      if (path.endsWith('/keys')) data = { items: [{ id: 42, name: 'Trial', key: 'sk-fixture', status: 'active', routing_mode: 'universal', group_id: null }], total: 1 }
      if (path.endsWith('/capabilities')) data = { models: models.map(id => ({ id, protocols: ['openai'], modalities: [kind], routes: [], ...(kind === 'image' ? { image_generation: [{ endpoint: '/v1/images/generations', aspect_ratios: ['1:1'], counts: [1], input_image: false, soft_aspect_ratio: true }] } : {}) })) }
      if (path.endsWith('/me/pricing-catalog')) data = { models: models.map(model_id => ({ model_id, billing_mode: kind, your_price: { currency: 'USD', per_image: 0.04, per_second: 0.01 } })) }
      return route.fulfill({ json: ok(data) })
    })
    await page.route('**/v1/models', route => route.fulfill({ json: { object: 'list', data: models.map(id => ({ id })) } }))
    let denial = 'trial_unpaid_media_blocked'
    for (const path of ['/v1/images/generations', '/v1/video/generations']) {
      await page.route(`**${path}`, route => route.fulfill({ status: 402, json: { error: {
        code: denial, type: 'invalid_request_error',
        // The code alone must work even if upstream wording changes.
        message: denial === 'trial_unpaid_media_blocked' ? 'Recharge required' : 'This group is not allowed to generate media'
      } } }))
    }
    await page.goto(`/studio?mode=${mode}&key=42`)
    if (mode === 'bakeoff') {
      await page.getByTestId('bakeoff-mode-image').click()
      await expect(page.getByTestId('bakeoff-tier')).toHaveCount(2)
      for (const tile of await page.getByTestId('bakeoff-tier').all()) await tile.click()
    } else {
      await page.getByTestId(`studio-${mode}-model`).click()
    }
    await page.locator('textarea').fill('A paper boat')
    const generate = page.getByTestId(mode === 'bakeoff' ? 'studio-bakeoff-run' : `studio-${mode}-generate`)
    await generate.click()
    const error = page.getByTestId(`studio-${mode}-error`)
    await expect(error).toContainText('图片与视频生成需完成充值后可用；文本模型仍可使用试用余额。')
    const topUp = error.getByRole('link', { name: '去充值' })
    await expect(topUp).toHaveAttribute('href', '/purchase')
    await page.screenshot({ path: info.outputPath(`${mode}-trial-recharge.png`), fullPage: true })
    // A different denial clears the recharge CTA on the same component.
    denial = 'permission_denied'
    await generate.click()
    await expect(error).toContainText('该分组未开通此类生成。')
    await expect(error.getByRole('link')).toHaveCount(0)
    denial = 'trial_unpaid_media_blocked'
    await generate.click()
    await topUp.click()
    await expect(page).toHaveURL(/\/purchase$/)
  })
}
