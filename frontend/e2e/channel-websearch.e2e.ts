import { expect, test } from '@playwright/test'
import type { Channel, UpdateChannelRequest } from '../src/api/admin/channels'

for (const globallyEnabled of [true, false]) {
  test(`channel web search saves platform switches with global enabled=${globallyEnabled}`, async ({ page }, testInfo) => {
    const user = { id: 1449, email: 'admin@example.test', role: 'admin', status: 'active', balance: 100, concurrency: 5, onboarding_tour_seen_at: '2026-10-29T00:00:00Z' }
    const channel: Channel = {
      id: 7, name: 'Platform settings', description: '', status: 'active',
      billing_model_source: 'channel_mapped', restrict_models: false,
      group_ids: [1, 2, 3], model_mapping: {}, model_pricing: [],
      features_config: { web_search_emulation: { anthropic: true, openai: true, command_code: true } },
      apply_pricing_to_account_stats: false, account_stats_pricing_rules: [],
      created_at: '2026-10-29T00:00:00Z', updated_at: '2026-10-29T00:00:00Z',
    }
    const saves: UpdateChannelRequest[] = []
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'fixture-session-only')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('tokenkey_locale', 'en')
      localStorage.setItem(`admin_guide_${user.id}_admin_v4_interactive`, 'true')
    }, user)
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', route => {
      const path = new URL(route.request().url()).pathname
      const ok = (data: unknown) => route.fulfill({ json: { code: 0, data } })
      if (path.endsWith('/auth/me')) return ok(user)
      if (path.endsWith('/settings/public')) return ok({ site_name: 'TokenKey', custom_menu_items: [], custom_endpoints: [] })
      if (path.endsWith('/settings/web-search-emulation')) return ok({ enabled: globallyEnabled, providers: [{ name: 'fixture' }] })
      if (path.endsWith('/groups/all')) return ok(['anthropic', 'openai', 'gemini'].map((platform, i) => ({ id: i + 1, name: `${platform} group`, platform, status: 'active' })))
      if (path.endsWith('/channels/7') && route.request().method() === 'PUT') {
        const payload = route.request().postDataJSON() as UpdateChannelRequest
        saves.push(payload)
        // API merge semantics have separate Go regression tests. This fixture
        // persists the submitted UI fields so reopening tests hydration.
        const inherited = channel.features_config?.web_search_emulation as Record<string, boolean>
        const incoming = payload.features_config?.web_search_emulation as Record<string, boolean>
        Object.assign(channel, payload, { features_config: { ...payload.features_config, web_search_emulation: { ...inherited, ...incoming } } })
        return ok(channel)
      }
      if (path.endsWith('/channels')) return ok({ items: [channel], total: 1, page: 1, page_size: 20 })
      return ok([])
    })
    await page.goto('/admin/channels')
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog.getByRole('button', { name: 'OpenAI', exact: true }).click()
    const toggle = dialog.getByRole('switch', { name: 'Web Search Emulation', exact: true })
    if (globallyEnabled) {
      await expect(toggle).toBeChecked()
      await toggle.click()
      await expect(toggle).not.toBeChecked()
      await dialog.getByRole('button', { name: 'Anthropic', exact: true }).click()
      await expect(toggle).toBeChecked()
      await dialog.getByRole('button', { name: 'Gemini', exact: true }).click()
      await expect(toggle).toHaveCount(0)
    } else {
      await expect(toggle).toHaveCount(0)
    }
    await page.screenshot({ path: testInfo.outputPath('channel-before-save.png'), fullPage: true })
    await dialog.getByRole('button', { name: 'Update', exact: true }).click()
    await expect(dialog).not.toBeVisible()
    expect(saves).toHaveLength(1)
    expect(saves[0].features_config?.web_search_emulation).toEqual({ anthropic: true, openai: !globallyEnabled })
    expect(saves[0].group_ids).toEqual([1, 2, 3])
    await page.getByRole('button', { name: 'Edit', exact: true }).click()
    await dialog.getByRole('button', { name: 'OpenAI', exact: true }).click()
    if (globallyEnabled) {
      await expect(toggle).not.toBeChecked()
      await toggle.click()
      await dialog.getByRole('button', { name: 'Basic Settings', exact: true }).click()
      await dialog.getByRole('checkbox', { name: 'OpenAI', exact: true }).uncheck()
      await dialog.getByRole('button', { name: 'Update', exact: true }).click()
      await expect(dialog).not.toBeVisible()
      expect(saves).toHaveLength(2)
      expect(saves[1].features_config?.web_search_emulation).toEqual({ anthropic: true, openai: false })
      expect(saves[1].group_ids).toEqual([1, 3])
    } else {
      await expect(toggle).toHaveCount(0)
    }
    await page.screenshot({ path: testInfo.outputPath('channel-after-save.png'), fullPage: true })
  })
}
