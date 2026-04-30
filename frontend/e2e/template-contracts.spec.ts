/**
 * Documents real "why I don't see this in the UI" causes: v-if gating in Vue SFCs,
 * not a broken renderer. Run: pnpm exec playwright test
 */
import { test, expect } from '@playwright/test'
import fs from 'fs'
import path from 'path'
import { fileURLToPath } from 'url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))

function read(rel: string): string {
  return fs.readFileSync(path.join(__dirname, '..', rel), 'utf8')
}

test.describe('Pool mode & failover-related UI (template facts)', () => {
  test('CreateAccountModal: pool mode sits inside apikey block that excludes newapi — fifth-platform create flow has no pool UI', () => {
    const vue = read('src/components/account/CreateAccountModal.vue')
    const gate =
      "form.type === 'apikey' && form.platform !== 'antigravity' && form.platform !== 'newapi'"
    const iGate = vue.indexOf(gate)
    const iPool = vue.indexOf('<!-- Pool Mode Section -->')
    expect(iGate, 'expected apikey+non-newapi gate').toBeGreaterThan(-1)
    expect(iPool, 'expected pool mode section').toBeGreaterThan(-1)
    expect(iPool, 'pool mode should appear after the gate (inside same gated region)').toBeGreaterThan(
      iGate
    )
    // Closing: next sibling at root of step-1 form after the gated <div> is not trivial to parse;
    // anchor instead: custom error codes follow pool inside the same apikey-non-newapi column in practice
    expect(vue.indexOf('<!-- Custom Error Codes Section -->')).toBeGreaterThan(iPool)
  })

  test('EditAccountModal: pool mode for apikey is outside newapi-only model-restriction div', () => {
    const vue = read('src/components/account/EditAccountModal.vue')
    const mr =
      '<div v-if="account.platform !== \'antigravity\' && account.platform !== \'newapi\'" class="border-t border-gray-200 pt-4 dark:border-dark-600">'
    const iMr = vue.indexOf(mr)
    const iPool = vue.indexOf('<!-- Pool Mode Section -->')
    expect(iMr).toBeGreaterThan(-1)
    expect(iPool).toBeGreaterThan(iMr)
  })

  test('EditAccountModal: OpenAI OAuth block has no pool mode (comment + structure)', () => {
    const vue = read('src/components/account/EditAccountModal.vue')
    const oauthOpen =
      '<div\n        v-if="account.platform === \'openai\' && account.type === \'oauth\'"\n        class="border-t border-gray-200 pt-4 dark:border-dark-600"\n      >'
    const i0 = vue.indexOf(oauthOpen)
    expect(i0).toBeGreaterThan(-1)
    const i1 = vue.indexOf('</div>\n\n      <!-- Upstream fields', i0)
    expect(i1).toBeGreaterThan(i0)
    const oauthSection = vue.slice(i0, i1)
    expect(oauthSection.includes('admin.accounts.poolMode')).toBe(false)
  })

  test('AccountNewApiPlatformFields: no pool_mode controls (must be composed by parent if needed)', () => {
    const vue = read('src/components/account/AccountNewApiPlatformFields.vue')
    expect(vue.toLowerCase()).not.toContain('pool_mode')
    expect(vue).not.toContain('admin.accounts.poolMode')
  })

  test('SettingsView: gateway tab does not expose max_account_switches (server config only)', () => {
    const vue = read('src/views/admin/SettingsView.vue')
    expect(vue).not.toMatch(/max_account_switches/i)
    expect(vue).not.toMatch(/maxAccountSwitch/i)
  })

  test('GroupsView: sticky_routing_mode is present for operators', () => {
    const vue = read('src/views/admin/GroupsView.vue')
    expect(vue).toContain('sticky_routing_mode')
  })
})
