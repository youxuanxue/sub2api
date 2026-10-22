import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'
import type { Channel } from '@/api/admin/channels'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { listPlatformIds } from '@/constants/platformCatalog'
import { apiToFormSections, formSectionsToApi } from '@/utils/channelFormConversion'

const cnConcretePlatforms = ['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'] as const

describe('Composite channel platform options', () => {
  it.each(cnConcretePlatforms)('preserves %s mapping when editing a channel', (platform) => {
    const mapping = { 'client-model': 'supplier-model' }
    const channel = {
      model_mapping: { [platform]: mapping },
      model_pricing: [],
      group_ids: [],
    } as unknown as Channel
    const sections = apiToFormSections(channel, [])
    expect(sections.find(section => section.platform === platform)?.enabled).toBe(true)
    expect(formSectionsToApi(sections).model_mapping[platform]).toEqual(mapping)
  })

  it('includes the CN concrete providers for pricing and model mapping', () => {
    const concreteValues = CONCRETE_PLATFORM_OPTIONS.map(option => option.value)
    expect(concreteValues).toEqual(expect.arrayContaining([...cnConcretePlatforms]))
    expect(GATEWAY_PLATFORMS).toEqual(expect.arrayContaining([...cnConcretePlatforms]))
    // Catalog list is backend-derived; TK ChannelsView currently pins GATEWAY_PLATFORMS
    // for newapi round-trip safety until builtin catalog includes TokenKey platforms.
    expect(listPlatformIds()).toEqual(
      expect.arrayContaining(['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go']),
    )
    const source = readFileSync(resolve('src/views/admin/ChannelsView.vue'), 'utf8')
    expect(source).toContain('GATEWAY_PLATFORMS')
    expect(source).not.toContain("import AppLayout from '@/components/layout/AppLayout.vue'")
  })
})
