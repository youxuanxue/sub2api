// vitest setup: polyfills for jsdom environment.
//
// jsdom historically does NOT implement window.matchMedia (active issue:
// https://github.com/jsdom/jsdom/issues/3552). Components that read viewport
// state at setup time (AccountUsageCell.vue line 482) crash on import. The
// polyfill returns an object that satisfies the MediaQueryList API surface
// the components actually use: `matches`, `addEventListener`/`removeEventListener`,
// and the legacy `addListener`/`removeListener`.
//
// Returning matches=true (desktop) keeps existing tests' viewport assumptions
// stable; tests that need a specific viewport can override window.matchMedia
// per-test before mounting.

if (typeof window !== 'undefined' && !window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: (query: string): MediaQueryList => ({
      matches: true,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }),
  })
}
