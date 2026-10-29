import { defineConfig } from '@playwright/test'
import preview from './playwright.public.config'

export default defineConfig({
  ...preview,
  testMatch: 'historical-billing.e2e.ts',
  outputDir: '.cache/historical-billing-results',
})
