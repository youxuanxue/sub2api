import { describe, expect, it } from 'vitest'

import { catalogBrowseModelCardRoute } from '../catalogBrowseModelCardRoute.tk'

describe('catalogBrowseModelCardRoute.tk', () => {
  it('routes authed users to quickstart with model only', () => {
    expect(catalogBrowseModelCardRoute('gemini-2.5-flash', { isAuthenticated: true })).toEqual({
      path: '/quickstart',
      query: { model: 'gemini-2.5-flash' },
    })
  })

  it('routes guests to pricing view with model preselected', () => {
    expect(catalogBrowseModelCardRoute('gpt-4o-mini', { isAuthenticated: false })).toEqual({
      path: '/models',
      query: { view: 'pricing', model: 'gpt-4o-mini' },
    })
  })
})
