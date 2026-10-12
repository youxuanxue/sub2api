import { defineConfig } from '@playwright/test'
import base from './playwright.config'

export default defineConfig({
  ...base,
  testMatch: '**/qa-retirement.e2e.ts',
  timeout: 60_000,
  use: { ...base.use, baseURL: 'http://127.0.0.1:4198' },
  webServer: {
    command: 'pnpm exec vite --host 127.0.0.1 --port 4198 --strictPort',
    url: 'http://127.0.0.1:4198',
    reuseExistingServer: false,
  },
})
