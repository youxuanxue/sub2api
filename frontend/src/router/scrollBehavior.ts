import type { RouterScrollBehavior } from 'vue-router'

/**
 * Keep the current viewport when a page only synchronizes its query string.
 * Views such as Quickstart update query params as async data becomes ready;
 * treating those updates as a new page otherwise jumps the user to the top.
 */
export const scrollBehavior: RouterScrollBehavior = (to, from, savedPosition) => {
  if (savedPosition) return savedPosition
  if (to.path === from.path) return false
  return { top: 0 }
}
