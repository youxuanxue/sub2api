/**
 * US-031 PR 2 P1-A — useOnboardingTour 普通用户解锁 + 服务端 seen_at 记忆
 *
 * Spec: docs/approved/user-cold-start.md §5 P1-A;
 *       .testing/user-stories/stories/US-031-onboarding-tour-unlock-for-regular-users.md
 *
 * What we test (and what we deliberately don't):
 *
 * - We do NOT spin up a real driver.js instance — driver.js manipulates the
 *   real DOM and is exhaustively covered by its own upstream tests; here
 *   we only care about the gating decision (auto-launch yes / no) and the
 *   server-side persistence call. Mocking `driver()` lets us count
 *   construction calls deterministically.
 *
 * - We do NOT test the visual popover layout — that is owned by the
 *   `onPopoverRender` block which is unchanged by this PR.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'

// ---- Mocks: driver.js (count constructor calls; expose .isActive()) ----
const driverInstanceFactory = () => ({
  isActive: vi.fn(() => true),
  destroy: vi.fn(),
  drive: vi.fn(),
  moveNext: vi.fn(),
  movePrevious: vi.fn(),
  getActiveElement: vi.fn(),
  getActiveIndex: vi.fn(() => 0)
})
const mockDriver = vi.fn(driverInstanceFactory)
vi.mock('driver.js', () => ({
  driver: (...args: unknown[]) => mockDriver(...args)
}))
vi.mock('driver.js/dist/driver.css', () => ({}))

// ---- Mocks: vue-i18n (composable expects useI18n().t) ----
vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

// ---- Mocks: steps catalog (skip i18n key churn in tests) ----
vi.mock('@/components/Guide/steps', () => ({
  getAdminSteps: vi.fn(() => [{ popover: { title: 'admin' } }]),
  getUserSteps: vi.fn(() => [{ popover: { title: 'user' } }])
}))

// ---- Mocks: api/user.markOnboardingTourSeen — spy for AC-005 ----
const markSeenSpy = vi.fn(() => Promise.resolve())
vi.mock('@/api/user', () => ({
  userAPI: {
    markOnboardingTourSeen: () => markSeenSpy()
  }
}))

// ---- Mocks: stores/auth + stores/onboarding (factory pattern; allow per-test override) ----
type FakeUser = {
  id: number
  role: 'admin' | 'user'
  onboarding_tour_seen_at?: string | null
} | null

let fakeUser: FakeUser = null
let fakeSimpleMode = false

vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get user() {
      return fakeUser
    },
    get isSimpleMode() {
      return fakeSimpleMode
    }
  })
}))

const onboardingStoreState = {
  driver: null as unknown
}
vi.mock('@/stores/onboarding', () => ({
  useOnboardingStore: () => ({
    isDriverActive: () => false,
    getDriverInstance: () => onboardingStoreState.driver,
    setDriverInstance: (d: unknown) => {
      onboardingStoreState.driver = d
    },
    setControlMethods: () => undefined,
    clearControlMethods: () => undefined
  })
}))

// Now import the composable AFTER all mocks are registered so the composable
// picks them up.
import { useOnboardingTour } from '@/composables/useOnboardingTour'
import { mount } from '@vue/test-utils'
import { defineComponent, h } from 'vue'

/**
 * Helper: mount a tiny host component that calls useOnboardingTour({autoStart: true})
 * inside its setup. This triggers the onMounted lifecycle which is what we're
 * actually testing.
 */
function mountTourHost() {
  return mount(defineComponent({
    setup() {
      useOnboardingTour({ autoStart: true })
      return () => h('div')
    }
  }))
}

describe('US-031 普通用户 Tour 解锁 (auto-start gate)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.useFakeTimers()
    mockDriver.mockClear()
    markSeenSpy.mockClear()
    localStorage.clear()
    onboardingStoreState.driver = null
  })

  afterEach(() => {
    vi.useRealTimers()
  })

  // AC-001 — fresh普通 user (role=user, seen_at=null, !simpleMode) → auto-start
  it('AC-001 普通用户首次自动启动 (seen_at == null)', async () => {
    fakeUser = { id: 1, role: 'user', onboarding_tour_seen_at: null }
    fakeSimpleMode = false

    const wrapper = mountTourHost()
    // wait for onMounted + autoStartTimer (1000ms TIMING.AUTO_START_DELAY_MS)
    vi.advanceTimersByTime(1500)
    await wrapper.vm.$nextTick()

    expect(mockDriver).toHaveBeenCalledTimes(1)
  })

  // AC-002 — admin first-time path is unchanged (regression guard)
  it('AC-002 admin 首次自动启动 (回归)', async () => {
    fakeUser = { id: 2, role: 'admin', onboarding_tour_seen_at: null }
    fakeSimpleMode = false

    const wrapper = mountTourHost()
    vi.advanceTimersByTime(1500)
    await wrapper.vm.$nextTick()

    expect(mockDriver).toHaveBeenCalledTimes(1)
  })

  // AC-003 — user already has server-side seen_at → must NOT auto-start
  it('AC-003 已看过不再自动启动 (seen_at != null)', async () => {
    fakeUser = {
      id: 3,
      role: 'user',
      onboarding_tour_seen_at: '2026-04-22T10:00:00Z'
    }
    fakeSimpleMode = false

    const wrapper = mountTourHost()
    vi.advanceTimersByTime(1500)
    await wrapper.vm.$nextTick()

    expect(mockDriver).not.toHaveBeenCalled()
  })

  // AC-004 — simple mode disables tour for everyone (existing behavior preserved)
  it('AC-004 simple mode 不启动', async () => {
    fakeUser = { id: 4, role: 'user', onboarding_tour_seen_at: null }
    fakeSimpleMode = true

    const wrapper = mountTourHost()
    vi.advanceTimersByTime(1500)
    await wrapper.vm.$nextTick()

    expect(mockDriver).not.toHaveBeenCalled()
  })

  // No user (e.g. logged out) → don't auto-launch
  it('未登录用户 (user == null) 不启动', async () => {
    fakeUser = null
    fakeSimpleMode = false

    const wrapper = mountTourHost()
    vi.advanceTimersByTime(1500)
    await wrapper.vm.$nextTick()

    expect(mockDriver).not.toHaveBeenCalled()
  })
})

describe('US-031 markAsSeen 触发服务端持久化 (AC-005)', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    mockDriver.mockClear()
    markSeenSpy.mockClear()
    localStorage.clear()
    onboardingStoreState.driver = null
  })

  // AC-005 — when the tour completion fires markAsSeen we MUST notify the
  // server so the seen_at survives cache clears / device switches.
  it('AC-005 调用 userAPI.markOnboardingTourSeen() once', async () => {
    fakeUser = { id: 5, role: 'user', onboarding_tour_seen_at: null }
    fakeSimpleMode = false

    // Mount and immediately invoke the public API to mark seen by reaching
    // into the composable's return value.
    let api: ReturnType<typeof useOnboardingTour> | null = null
    mount(defineComponent({
      setup() {
        api = useOnboardingTour({ autoStart: false })
        return () => h('div')
      }
    }))

    // Trigger the seen-marker as the driver's onCloseClick / onNextClick
    // would have done in production.
    api!.markAsSeen()

    expect(markSeenSpy).toHaveBeenCalledTimes(1)
  })

  // Defensive: when no user is logged in we MUST NOT post to the server
  // (the request would 401 and pollute logs).
  it('未登录时 markAsSeen 不调用服务端', () => {
    fakeUser = null
    fakeSimpleMode = false

    let api: ReturnType<typeof useOnboardingTour> | null = null
    mount(defineComponent({
      setup() {
        api = useOnboardingTour({ autoStart: false })
        return () => h('div')
      }
    }))

    api!.markAsSeen()

    expect(markSeenSpy).not.toHaveBeenCalled()
  })
})
