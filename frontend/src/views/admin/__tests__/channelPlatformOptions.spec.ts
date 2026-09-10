import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'

describe('Composite channel platform options', () => {
  it('includes the CN concrete providers for pricing and model mapping', () => {
    const source = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
    expect(source).toMatch(/const platformOrder:[^=]+= GATEWAY_PLATFORMS/)
    expect(GATEWAY_PLATFORMS).toEqual(expect.arrayContaining(['kimi', 'zhipu', 'deepseek']))
  })
})
