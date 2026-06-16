import { test, expect, type Page } from '@playwright/test'

// Real end-to-end acceptance for the Media Studio. Generation hits real upstream
// (Vertex imagen/veo via the prod-copied service account), so timeouts are
// generous and the suite runs serially (workers:1) to keep upstream load + cost
// bounded.
const EMAIL = process.env.E2E_EMAIL || 'admin@tokenkey.local'
const PASS = process.env.E2E_PASSWORD || 'Admin12345!'

async function login(page: Page): Promise<void> {
  await page.goto('/login')
  await page.locator('input[type=email]').first().fill(EMAIL)
  await page.locator('input[type=password]').first().fill(PASS)
  await page.locator('button[type=submit]').first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20_000 })
}

test('image generation — real Vertex imagen, price on button', async ({ page }) => {
  await login(page)
  await page.goto('/studio')
  // Studio now lands on the Chat tab — switch to Image explicitly (the modality-
  // aware picker re-targets a key whose group serves imagen).
  await page.getByTestId('studio-mode-image').click()

  const gen = page.getByTestId('studio-image-generate')
  await expect(gen).toContainText('$') // killer A: the price is on the Generate button
  await gen.click()

  // A real image lands in the result grid (b64 from Vertex → data: URI).
  await expect(page.locator('figure img').first()).toBeVisible({ timeout: 120_000 })
  await expect(page.getByTestId('studio-image-error')).toHaveCount(0)
  await page.screenshot({ path: 'e2e/artifacts/01-image-result.png', fullPage: true })
})

test('video generation — real Vertex veo, async timeline → in-page playback', async ({ page }) => {
  test.setTimeout(300_000)
  await login(page)
  await page.goto('/studio')
  await page.getByTestId('studio-mode-video').click()

  const gen = page.getByTestId('studio-video-generate')
  await expect(gen).toContainText('$')
  // "failed videos are refunded in full" trust line is shown next to the money.
  await expect(page.getByText(/refunded in full|失败的视频全额退款/i).first()).toBeVisible()

  await gen.click()
  await page.screenshot({ path: 'e2e/artifacts/02-video-processing.png', fullPage: true })

  // The async task plays in-page when the upstream render completes.
  await expect(page.locator('video').first()).toBeVisible({ timeout: 280_000 })
  await page.screenshot({ path: 'e2e/artifacts/03-video-ready.png', fullPage: true })
})

test('bake-off — one prompt across multiple image models', async ({ page }) => {
  test.setTimeout(180_000)
  await login(page)
  await page.goto('/studio')
  await page.getByTestId('studio-mode-bakeoff').click()
  await page.getByTestId('bakeoff-mode-image').click() // image mode (cheap)

  const tiers = page.getByTestId('bakeoff-tier')
  await tiers.nth(0).click()
  await tiers.nth(1).click()

  await page.getByTestId('studio-bakeoff-run').waitFor()
  // type a prompt then run
  await page.locator('textarea').first().fill('a calm mountain lake at dawn, soft light')
  const run = page.getByTestId('studio-bakeoff-run')
  await expect(run).toBeEnabled()
  await run.click()

  await expect(page.getByTestId('bakeoff-panel')).toHaveCount(2)
  await expect(page.getByTestId('bakeoff-panel').locator('img')).toHaveCount(2, { timeout: 150_000 })
  await page.screenshot({ path: 'e2e/artifacts/04-bakeoff.png', fullPage: true })
})

test('balance gating — cost on button + balance panel visible', async ({ page }) => {
  await login(page)
  await page.goto('/studio')
  await page.getByTestId('studio-mode-image').click() // default tab is now Chat
  // Price on the generate button + a balance readout in the header/cost panel.
  await expect(page.getByTestId('studio-image-generate')).toContainText('$')
  await expect(page.getByText(/Balance|余额/i).first()).toBeVisible()
  await page.screenshot({ path: 'e2e/artifacts/05-gating.png', fullPage: true })
})

test('chat — Studio lands on the Chat tab with a working composer', async ({ page }) => {
  await login(page)
  await page.goto('/studio')
  // Chat is the default landing modality (zero-cost, near-universal key support).
  const chatTab = page.getByTestId('studio-mode-chat')
  await expect(chatTab).toHaveAttribute('aria-selected', 'true')
  // The composer renders and is ready (a chat-serving key was auto-picked).
  await expect(page.locator('textarea').first()).toBeVisible()
  await page.screenshot({ path: 'e2e/artifacts/06-chat.png', fullPage: true })
})
