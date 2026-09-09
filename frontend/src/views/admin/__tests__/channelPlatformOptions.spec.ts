import { describe, expect, it } from 'vitest'
import type { Channel } from '@/api/admin/channels'
import { apiToFormSections, formSectionsToApi } from '@/utils/channelFormConversion'

describe('Composite channel platform options', () => {
  it.each(['kimi', 'zhipu', 'deepseek', 'minimax'] as const)('preserves %s mapping when editing a channel', (platform) => {
    const mapping = { 'client-model': 'supplier-model' }
    const channel = { model_mapping: { [platform]: mapping }, model_pricing: [], group_ids: [] } as unknown as Channel
    const sections = apiToFormSections(channel, [])
    expect(sections.find(section => section.platform === platform)?.enabled).toBe(true)
    expect(formSectionsToApi(sections).model_mapping[platform]).toEqual(mapping)
  })
})
