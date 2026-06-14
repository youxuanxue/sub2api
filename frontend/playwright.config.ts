import { defineConfig, devices } from '@playwright/test'

// TK Media Studio e2e — drives the REAL /studio UI against a locally deployed
// backend wired to real upstream (Vertex) credentials copied from prod.
// Chromium runs with --no-proxy-server so it reaches localhost:8080 directly
// even when the shell has an HTTP(S)_PROXY exported (fingerprint egress chain).
export default defineConfig({
  testDir: './e2e',
  testMatch: '**/*.e2e.ts',
  timeout: 180_000,
  expect: { timeout: 15_000 },
  retries: 0,
  workers: 1,
  reporter: [['list'], ['html', { open: 'never', outputFolder: 'e2e/report' }]],
  outputDir: 'e2e/artifacts',
  use: {
    baseURL: process.env.E2E_BASE_URL || 'http://localhost:8080',
    headless: true,
    screenshot: 'on',
    video: 'retain-on-failure',
    trace: 'retain-on-failure',
    launchOptions: { args: ['--no-proxy-server'] },
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
