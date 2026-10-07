import { defineConfig } from '@playwright/test'
import preview from './playwright.public.config'

// Verify the real production workbook chunk and downloaded file, with local API fixtures.
export default defineConfig({
  ...preview,
  testMatch: 'usage-export.e2e.ts',
  outputDir: '.cache/usage-export-results',
})
