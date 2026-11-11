import { defineConfig } from '@playwright/test'
import base from './playwright.quickstart.config'

export default defineConfig({
  ...base,
  testMatch: [
    '**/public-quickstart.e2e.ts',
    '**/upstream-merge-settings.e2e.ts',
    '**/account-usage-windows.e2e.ts'
  ]
})
