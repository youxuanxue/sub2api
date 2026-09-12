import { createRequire } from 'node:module'
const { test, expect } = createRequire(new URL('../../../frontend/package.json', import.meta.url))('@playwright/test')

test('one click opens a clean Edge URL and only the child receives the session', async ({ page }, testInfo) => {
  const parentResponses = []
  page.on('response', async response => { if (response.url().endsWith('/api/handoff')) parentResponses.push(await response.json()) })
  await page.goto('/')
  const popup = page.waitForEvent('popup')
  await page.getByRole('button', { name: '进入 Edge', exact: true }).click()
  const child = await popup
  await expect(child.locator('#status')).toHaveText('已进入 Edge 管理台')
  expect(child.url()).toBe('http://127.0.0.1:4312/admin/edge-handoff')
  expect(await child.evaluate(() => window.opener === null)).toBe(true)
  expect(parentResponses).toHaveLength(1)
  expect(Object.keys(parentResponses[0]).sort()).toEqual(['attempt', 'code'])
  expect(await page.evaluate(() => localStorage.length + sessionStorage.length)).toBe(0)
  await child.screenshot({ path: testInfo.outputPath('edge-connected.png') })
})

test('another window cannot substitute its challenge or consume the handoff', async ({ page }) => {
  let mints = 0
  page.on('request', request => { if (request.url().endsWith('/api/handoff')) mints++ })
  // Delay the legitimate child script while a different Edge-origin frame sends
  // a forged ready message. Exact source binding must reject it.
  let release
  const gate = new Promise(resolve => { release = resolve })
  await page.context().route('http://127.0.0.1:4312/browser.mjs', async route => { await gate; await route.continue() })
  await page.goto('/')
  const popup = page.waitForEvent('popup')
  await page.getByRole('button', { name: '进入 Edge', exact: true }).click()
  const child = await popup
  await page.evaluate(() => { const frame = document.createElement('iframe'); frame.src = 'http://127.0.0.1:4312/login'; document.body.append(frame) })
  const frame = await expect.poll(() => page.frames().find(item => item.url().endsWith('/login'))?.url()).toBe('http://127.0.0.1:4312/login').then(() => page.frames().find(item => item.url().endsWith('/login')))
  await frame.evaluate(() => parent.postMessage({ type: 'edge-ready', attempt: 'A'.repeat(43), challenge: 'B'.repeat(43) }, 'http://127.0.0.1:4311'))
  // A same-origin roundtrip follows the forged message in the event queue.
  await page.evaluate(() => new Promise(resolve => { const channel = new MessageChannel(); channel.port1.onmessage = () => { channel.port1.close(); channel.port2.close(); resolve() }; channel.port2.postMessage(null) }))
  expect(mints).toBe(0)
  release()
  await expect(child.locator('#status')).toHaveText('已进入 Edge 管理台')
  expect(mints).toBe(1)
})

test('unavailable handoff retains an actionable manual login', async ({ page }) => {
  await page.route('**/api/handoff', route => route.fulfill({ status: 503, json: { error: 'unavailable' } }))
  await page.goto('/')
  await page.getByRole('button', { name: '进入 Edge', exact: true }).click()
  await expect(page.locator('#status')).toContainText('可以重试')
  await expect(page.getByRole('button', { name: '进入 Edge', exact: true })).toBeEnabled()
  await expect(page.getByRole('link', { name: '直接登录 Edge' })).toHaveAttribute('href', 'http://127.0.0.1:4312/login')
})
