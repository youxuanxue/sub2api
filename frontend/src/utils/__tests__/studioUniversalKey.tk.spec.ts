import { describe, expect, it } from 'vitest'
import {
  entitledModelIds,
  isUniversalKey,
  priceMapFromPublicCatalog,
} from '../studioUniversalKey.tk'
import type { ApiKey } from '@/types'

describe('studioUniversalKey', () => {
  it('detects universal keys by routing_mode or missing group', () => {
    expect(isUniversalKey({ id: 1, routing_mode: 'universal' } as ApiKey)).toBe(true)
    expect(isUniversalKey({ id: 2, group_id: null, group: null } as ApiKey)).toBe(true)
    expect(isUniversalKey({ id: 3, group: { id: 1, name: 'g' } } as ApiKey)).toBe(false)
  })

  it('builds entitled model ids from authorized_groups_by_model', () => {
    const ids = entitledModelIds({
      authorized_groups_by_model: {
        'veo-3.1-generate-001': [{ group_id: 1, group_name: 'vertex' }],
        'gemini-3.1-flash-image': [{ group_id: 2, group_name: 'gemini' }],
      },
    } as never)
    expect(ids.has('veo-3.1-generate-001')).toBe(true)
    expect(ids.has('gemini-3.1-flash-image')).toBe(true)
  })

  it('intersects public catalog prices with entitled ids', () => {
    const map = priceMapFromPublicCatalog(
      [
        {
          model_id: 'veo-3.1-generate-001',
          pricing: { billing_mode: 'video', output_cost_per_second: 0.6 },
        },
        {
          model_id: 'gpt-4o',
          pricing: { output_cost_per_image: 0.01 },
        },
      ] as never,
      new Set(['veo-3.1-generate-001'])
    )
    expect(map.get('veo-3.1-generate-001')?.perSecond).toBe(0.6)
    expect(map.get('veo-3.1-generate-001')?.billingMode).toBe('video')
    expect(map.has('gpt-4o')).toBe(false)
  })
})
