import { createRequire } from 'node:module'
const { defineConfig } = createRequire(new URL('../../../frontend/package.json', import.meta.url))('@playwright/test')
export default defineConfig({
  testDir: '.', testMatch: 'browser.test.mjs', workers: 1, retries: 0,
  outputDir: '../../../.cache/edge-handoff-prototype',
  use: { baseURL: 'http://127.0.0.1:4311', trace: 'off', video: 'off', screenshot: 'only-on-failure' },
  webServer: { command: 'node server.mjs', cwd: new URL('.', import.meta.url).pathname, url: 'http://127.0.0.1:4311', reuseExistingServer: false },
})
