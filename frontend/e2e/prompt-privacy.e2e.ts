import { expect, test } from '@playwright/test'

const canary = 'LEGACY_RAW_PROMPT_CANARY_MUST_NOT_RENDER'
const user = {
  id: 7, username: 'privacy-admin', email: 'privacy@example.test', role: 'admin',
  balance: 100, concurrency: 5, status: 'active', allowed_groups: [],
  onboarding_tour_seen_at: '2026-01-01T00:00:00Z',
}
const event = {
  id: 1, job_id: 1, decision: 'critical', risk_level: 'critical', action: 'Block',
  categories: ['pii'], matched_scanners: ['pii'], scanner_scores: { pii: 1 },
  scanner_evidence: { pii: 'redacted' }, scanner_backend: 'guard', scanner_version: '1',
  guard_endpoint_id: 'guard-1', policy_id: 'priority', policy_version: 1, config_version: 1,
  chunk_total: 1, latency_ms: 10, issue_summaries: [], created_at: '2026-11-12T00:00:00Z',
  snapshot: {
    request_id: 'privacy-req', user_id: 1, username: 'alice', user_email: 'alice@example.test',
    api_key_id: 2, api_key_name: 'alice-key', group_id: 3, group_name: 'Alpha',
    provider: 'openai', endpoint: '/v1/chat/completions', protocol: 'openai_chat',
    model: 'test-model', prompt_hash: 'a'.repeat(64), redacted_preview: 'SAFE_REDACTED_PREVIEW',
    full_prompt: canary, prompt_length: 100, message_count: 1, stage: 'http',
  },
}

for (const width of [1440, 390]) {
  test(`prompt audit hides legacy raw text at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 })
    await page.addInitScript(user => {
      localStorage.setItem('auth_token', 'synthetic-privacy-token')
      localStorage.setItem('auth_user', JSON.stringify(user))
      localStorage.setItem('locale', 'en')
      localStorage.setItem('admin_guide_7_admin_v4_interactive', 'true')
    }, user)
    await page.route('**/setup/status', route => route.fulfill({ json: { code: 0, data: { needs_setup: false } } }))
    await page.route('**/api/v1/**', async route => {
      const path = new URL(route.request().url()).pathname
      let data: unknown = {}
      if (path.endsWith('/auth/me')) data = user
      else if (path.endsWith('/prompt-audit/events/1')) data = event
      else if (path.endsWith('/prompt-audit/events')) data = { items: [event], total: 1, page: 1, page_size: 20, pages: 1 }
      else if (path.endsWith('/prompt-audit/config')) data = {
        enabled: false, blocking_enabled: false, store_pass_events: false, effective_mode: 'off',
        strategy: 'priority', worker_count: 4, queue_capacity: 100, scanners: [],
        all_groups: true, group_ids: [], endpoints: [], config_version: 1,
      }
      else if (path.endsWith('/prompt-audit/runtime')) data = {
        process_status: 'disabled', effective_mode: 'off', expected_config_version: 1, active_config_version: 1,
        worker_total: 0, worker_active: 0, queue_capacity: 100, queue: {}, endpoints: {}, guard_metrics: {},
      }
      else if (path.endsWith('/all')) data = []
      else if (path.includes('/announcements')) data = { items: [], total: 0 }
      await route.fulfill({ json: { code: 0, data } })
    })
    await page.goto('/admin/prompt-audit')
    const row = page.locator('[data-test="event-1"]')
    await expect(row).toBeVisible()
    await row.getByRole('button', { name: 'View', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog.locator('[data-test="summary-prompt-full"]')).toHaveText('SAFE_REDACTED_PREVIEW')
    for (const tab of await dialog.getByRole('tab').all()) {
      await tab.click()
      await expect(dialog).not.toContainText(canary)
    }
    await dialog.getByRole('tab').nth(1).click()
    await expect(dialog.locator('[data-test="risk-prompt-full"]')).toHaveText('SAFE_REDACTED_PREVIEW')
    await page.screenshot({ path: `e2e/artifacts/prompt-privacy-${width}.png`, fullPage: true })
  })
}
