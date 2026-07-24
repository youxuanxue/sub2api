import { describe, expect, it } from 'vitest'
import { createMemoryHistory, createRouter } from 'vue-router'

describe('catalog hub routing', () => {
  it('redirects /pricing to /models?view=pricing and preserves query', async () => {
    const router = createRouter({
      history: createMemoryHistory(),
      routes: [
        {
          path: '/models',
          name: 'ModelMarketplace',
          component: { template: '<div />' },
        },
        {
          path: '/pricing',
          name: 'Pricing',
          redirect: (to) => ({
            path: '/models',
            query: { ...to.query, view: 'pricing' },
            hash: to.hash,
          }),
        },
      ],
    })

    await router.push('/pricing?model=gpt-4o-mini')
    await router.isReady()

    expect(router.currentRoute.value.path).toBe('/models')
    expect(router.currentRoute.value.query).toEqual({ model: 'gpt-4o-mini', view: 'pricing' })
  })
})
