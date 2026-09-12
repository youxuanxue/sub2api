import { defineConfig } from '@playwright/test'
export default defineConfig({
  testDir: './e2e', testMatch: 'edge-handoff.e2e.ts', workers: 1, retries: 0,
  timeout: 30000, expect: { timeout: 8000 }, reporter: 'list',
  outputDir: '.cache/edge-handoff-e2e',
  use: { baseURL: 'http://127.0.0.1:4321', locale: 'en-US', trace: 'off', video: 'off', screenshot: 'off' },
  webServer: { command: 'node e2e/edge-handoff-server.mjs', url: 'http://127.0.0.1:4321/health', timeout: 120000, reuseExistingServer: false },
})
