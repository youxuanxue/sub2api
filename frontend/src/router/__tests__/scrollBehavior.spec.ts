import { describe, expect, it } from 'vitest'
import { scrollBehavior } from '../scrollBehavior'

const location = (path: string, query: string) => ({
  path,
  query: {},
  hash: '',
  fullPath: `${path}${query}`,
  matched: [],
  meta: {},
  name: undefined,
  params: {},
  redirectedFrom: undefined,
})

describe('scrollBehavior', () => {
  it('preserves scroll position when only query params change', () => {
    expect(scrollBehavior(location('/quickstart', '?client=claude-code'), location('/quickstart', ''), null))
      .toBe(false)
  })

  it('scrolls to the top when navigating to a different path', () => {
    expect(scrollBehavior(location('/keys', ''), location('/quickstart', ''), null)).toEqual({ top: 0 })
  })

  it('restores the browser history position when provided', () => {
    const savedPosition = { left: 12, top: 480 }
    expect(scrollBehavior(location('/quickstart', ''), location('/keys', ''), savedPosition))
      .toEqual(savedPosition)
  })
})
