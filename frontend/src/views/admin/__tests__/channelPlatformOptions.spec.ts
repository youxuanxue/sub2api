import { describe, expect, it } from 'vitest'
import type { Channel } from '@/api/admin/channels'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import { CONCRETE_PLATFORM_OPTIONS } from '@/constants/platforms'
import { apiToFormSections, formSectionsToApi } from '@/utils/channelFormConversion'

const cnConcretePlatforms = ['kimi', 'zhipu', 'deepseek', 'minimax', 'opencode_go'] as const

describe('Composite channel platform options', () => {
  it.each(cnConcretePlatforms)('preserves %s mapping when editing a channel', (platform) => {
    const mapping = { 'client-model': 'supplier-model' }
    const channel = { model_mapping: { [platform]: mapping }, model_pricing: [], group_ids: [] } as unknown as Channel
    const sections = apiToFormSections(channel, [])
    expect(sections.find(section => section.platform === platform)?.enabled).toBe(true)
    expect(formSectionsToApi(sections).model_mapping[platform]).toEqual(mapping)
  })

  it('includes the CN concrete providers for pricing and model mapping', () => {
    const concreteValues = CONCRETE_PLATFORM_OPTIONS.map((option) => option.value)
    expect(concreteValues).toEqual(expect.arrayContaining([...cnConcretePlatforms]))
    expect(GATEWAY_PLATFORMS).toEqual(expect.arrayContaining([...cnConcretePlatforms]))
  })
})
