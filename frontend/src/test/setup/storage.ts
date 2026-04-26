import { beforeEach } from 'vitest'

class MemoryStorage implements Storage {
  private store = new Map<string, string>()

  get length() {
    return this.store.size
  }

  clear() {
    this.store.clear()
  }

  getItem(key: string) {
    return this.store.has(key) ? this.store.get(key)! : null
  }

  key(index: number) {
    return Array.from(this.store.keys())[index] ?? null
  }

  removeItem(key: string) {
    this.store.delete(key)
  }

  setItem(key: string, value: string) {
    this.store.set(key, String(value))
  }
}

const installStorage = (name: 'localStorage' | 'sessionStorage') => {
  const storage = new MemoryStorage()
  Object.defineProperty(window, name, {
    value: storage,
    configurable: true,
    writable: true,
  })
  Object.defineProperty(globalThis, name, {
    value: storage,
    configurable: true,
    writable: true,
  })
  return storage
}

const createMediaQueryList = (query: string): MediaQueryList => ({
  matches: false,
  media: query,
  onchange: null,
  addListener: () => {},
  removeListener: () => {},
  addEventListener: () => {},
  removeEventListener: () => {},
  dispatchEvent: () => false,
})

Object.defineProperty(window, 'matchMedia', {
  value: (query: string) => createMediaQueryList(query),
  configurable: true,
  writable: true,
})
Object.defineProperty(globalThis, 'matchMedia', {
  value: window.matchMedia,
  configurable: true,
  writable: true,
})

const local = installStorage('localStorage')
const session = installStorage('sessionStorage')

beforeEach(() => {
  local.clear()
  session.clear()
})
