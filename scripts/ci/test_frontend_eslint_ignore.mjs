#!/usr/bin/env node

import assert from 'node:assert/strict'
import { createRequire } from 'node:module'
import { test } from 'node:test'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const repositoryRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')
const frontendRoot = resolve(repositoryRoot, 'frontend')
const requireFromFrontend = createRequire(resolve(frontendRoot, 'package.json'))
const { ESLint } = requireFromFrontend('eslint')

test('ESLint ignores timestamped config modules created during parallel Vitest startup', async () => {
  const eslint = new ESLint({ cwd: frontendRoot })
  const volatileConfig = resolve(
    frontendRoot,
    'vitest.config.ts.timestamp-123456-deadbeef.mjs'
  )

  assert.equal(await eslint.isPathIgnored(volatileConfig), true)
})
