import {
  ref,
  computed,
  watch,
  onMounted,
  onBeforeUnmount,
  onUnmounted,
  type Ref
} from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { Account, AccountUsageInfo } from '@/types'
import { buildOpenAIUsageRefreshKey } from '@/utils/accountUsageRefresh'
import { enqueueUsageRequest } from '@/utils/usageLoadQueue'
import type { AccountUsageCellProps } from '../accountUsageCellProps'
import {
  canSelfFetchUsage as accountCanSelfFetchUsage,
  isBatchPassiveCapable,
  usesLocalUsageWindows,
  usesPassiveUsageOnMount as accountUsesPassiveUsageOnMount
} from '@/utils/accountUsageBatch.tk'

// Module-level cache shared across all usage cell instances
const _usageCache = new Map<number, { data: AccountUsageInfo; ts: number }>()
export const USAGE_CACHE_TTL = 5 * 60 * 1000 // 5 minutes

export function clearAccountUsageCache(accountId?: number) {
  if (accountId === undefined) {
    _usageCache.clear()
    return
  }
  _usageCache.delete(accountId)
}

const desktopViewportQuery = '(min-width: 768px)'

function canFetchUsageForAccount(props: AccountUsageCellProps): boolean {
  return accountCanSelfFetchUsage(props.account)
}

function shouldAutoFetchUsageForAccount(props: AccountUsageCellProps): boolean {
  if (props.usageOverride !== undefined) return false
  return canFetchUsageForAccount(props)
}

export function useAccountUsageFetch(
  props: AccountUsageCellProps,
  rootRef: Ref<HTMLElement | null>,
  options?: {
    enableOpenAIRefreshKeyWatch?: boolean
  }
) {
  const { t } = useI18n()

  const unmounted = ref(false)
  onBeforeUnmount(() => {
    unmounted.value = true
  })

  const loading = ref(false)
  const activeQueryLoading = ref(false)
  const error = ref<string | null>(null)
  const usageInfo = ref<AccountUsageInfo | null>(
    props.usageOverride !== undefined ? props.usageOverride : null
  )

  watch(
    () => props.usageOverride,
    (v) => {
      if (v !== undefined) usageInfo.value = v
    },
    { immediate: true }
  )

  const isDesktopViewport = ref(
    typeof window === 'undefined' ? true : window.matchMedia(desktopViewportQuery).matches
  )
  const hasEnteredViewport = ref(false)
  const pendingAutoLoad = ref(false)
  const pendingAutoLoadSource = ref<'passive' | 'active' | undefined>(undefined)

  let desktopViewportMediaQuery: MediaQueryList | null = null
  let desktopViewportListener: ((event: MediaQueryListEvent) => void) | null = null
  let visibilityObserver: IntersectionObserver | null = null

  const shouldFetchUsage = computed(() => shouldAutoFetchUsageForAccount(props))
  const canFetchUsage = computed(() => canFetchUsageForAccount(props))

  const shouldAutoLoadUsageOnMount = computed(() => shouldFetchUsage.value)

  const shouldLazyLoadOnMobile = computed(() => {
    return shouldFetchUsage.value && !isDesktopViewport.value
  })

  const openAIUsageRefreshKey = computed(() => buildOpenAIUsageRefreshKey(props.account))

  const loadUsage = async (loadOptions?: {
    source?: 'passive' | 'active'
    bypassCache?: boolean
  }) => {
    if (!canFetchUsage.value) return

    if (!loadOptions?.bypassCache) {
      const cached = _usageCache.get(props.account.id)
      if (cached && Date.now() - cached.ts < USAGE_CACHE_TTL) {
        usageInfo.value = cached.data
        loading.value = false
        return
      }
    }

    loading.value = true
    error.value = null

    try {
      const fetchFn = () => adminAPI.accounts.getUsage(props.account.id, loadOptions?.source)
      const result = await enqueueUsageRequest(props.account, fetchFn)
      if (!unmounted.value) {
        usageInfo.value = result
        _usageCache.set(props.account.id, { data: result, ts: Date.now() })
      }
    } catch (e: unknown) {
      if (!unmounted.value) {
        error.value = t('common.error')
        console.error('Failed to load usage:', e)
      }
    } finally {
      if (!unmounted.value) loading.value = false
    }
  }

  const flushPendingAutoLoad = () => {
    if (!pendingAutoLoad.value) return
    const source = pendingAutoLoadSource.value
    pendingAutoLoad.value = false
    pendingAutoLoadSource.value = undefined
    loadUsage({ source }).catch((e) => {
      console.error('Failed to load deferred usage:', e)
    })
  }

  const requestAutoLoad = (source?: 'passive' | 'active') => {
    if (!shouldFetchUsage.value) return
    if (shouldLazyLoadOnMobile.value && !hasEnteredViewport.value) {
      pendingAutoLoad.value = true
      pendingAutoLoadSource.value = source
      return
    }
    loadUsage({ source }).catch((e) => {
      console.error('Failed to auto load usage:', e)
    })
  }

  const detachVisibilityObserver = () => {
    visibilityObserver?.disconnect()
    visibilityObserver = null
  }

  const attachVisibilityObserver = () => {
    detachVisibilityObserver()
    if (!shouldLazyLoadOnMobile.value || hasEnteredViewport.value) return
    if (typeof window === 'undefined' || typeof IntersectionObserver === 'undefined') {
      hasEnteredViewport.value = true
      flushPendingAutoLoad()
      return
    }
    if (!rootRef.value) return

    visibilityObserver = new IntersectionObserver(
      (entries) => {
        if (!entries.some((entry) => entry.isIntersecting)) return
        hasEnteredViewport.value = true
        detachVisibilityObserver()
        flushPendingAutoLoad()
      },
      {
        root: null,
        rootMargin: '200px 0px',
        threshold: 0.01
      }
    )
    visibilityObserver.observe(rootRef.value)
  }

  const loadActiveUsage = async () => {
    activeQueryLoading.value = true
    try {
      usageInfo.value = await adminAPI.accounts.getUsage(props.account.id, 'active', true)
    } catch (e: unknown) {
      console.error('Failed to load active usage:', e)
    } finally {
      activeQueryLoading.value = false
    }
  }

  onMounted(() => {
    if (typeof window !== 'undefined') {
      desktopViewportMediaQuery = window.matchMedia(desktopViewportQuery)
      isDesktopViewport.value = desktopViewportMediaQuery.matches
      desktopViewportListener = (event: MediaQueryListEvent) => {
        isDesktopViewport.value = event.matches
      }
      if (typeof desktopViewportMediaQuery.addEventListener === 'function') {
        desktopViewportMediaQuery.addEventListener('change', desktopViewportListener)
      } else {
        desktopViewportMediaQuery.addListener(desktopViewportListener)
      }
    }

    if (!shouldAutoLoadUsageOnMount.value) return
    const source = accountUsesPassiveUsageOnMount(props.account) ? 'passive' : undefined
    requestAutoLoad(source)
  })

  if (options?.enableOpenAIRefreshKeyWatch) {
    watch(openAIUsageRefreshKey, (nextKey, prevKey) => {
      if (!prevKey || nextKey === prevKey) return
      if (props.account.platform !== 'openai' || props.account.type !== 'oauth') return

      _usageCache.delete(props.account.id)
      requestAutoLoad()
    })
  }

  watch(
    () => props.manualRefreshToken,
    (nextToken, prevToken) => {
      if (nextToken === prevToken) return
      if (!canFetchUsage.value) return

      // Kiro: explicit 刷新 pulls live credits once (#1031). Must run before the
      // batch-passive early return (kiro is batch-capable but not batch-only on refresh).
      if (props.account.platform === 'kiro') {
        _usageCache.delete(props.account.id)
        loadActiveUsage().catch((e) => {
          console.error('Failed to refresh kiro usage after manual refresh:', e)
        })
        return
      }

      // AccountsView already refreshes Anthropic/OpenAI/Kiro passive usage through the
      // batch endpoint before bumping this token; do not reintroduce per-row fan-out.
      if (props.usageOverride !== undefined && isBatchPassiveCapable(props.account)) return

      const source = accountUsesPassiveUsageOnMount(props.account) ? 'passive' : undefined
      _usageCache.delete(props.account.id)
      loadUsage({ source, bypassCache: true }).catch((e) => {
        console.error('Failed to refresh usage after manual refresh:', e)
      })
    }
  )

  watch(
    [rootRef, shouldLazyLoadOnMobile],
    () => {
      if (shouldLazyLoadOnMobile.value) {
        attachVisibilityObserver()
        return
      }
      detachVisibilityObserver()
    },
    { immediate: true, flush: 'post' }
  )

  watch(isDesktopViewport, (isDesktop) => {
    if (isDesktop) {
      detachVisibilityObserver()
      hasEnteredViewport.value = true
      flushPendingAutoLoad()
      return
    }
    hasEnteredViewport.value = false
    attachVisibilityObserver()
  })

  onUnmounted(() => {
    detachVisibilityObserver()
    if (desktopViewportMediaQuery && desktopViewportListener) {
      if (typeof desktopViewportMediaQuery.removeEventListener === 'function') {
        desktopViewportMediaQuery.removeEventListener('change', desktopViewportListener)
      } else {
        desktopViewportMediaQuery.removeListener(desktopViewportListener)
      }
    }
    desktopViewportListener = null
    desktopViewportMediaQuery = null
  })

  return {
    loading,
    activeQueryLoading,
    error,
    usageInfo,
    loadUsage,
    loadActiveUsage,
    shouldFetchUsage
  }
}

export function showUsageWindowsForAccount(account: Account): boolean {
  if (
    account.platform === 'gemini' ||
    account.platform === 'kiro' ||
    usesLocalUsageWindows(account)
  ) {
    return true
  }
  return account.type === 'oauth' || account.type === 'setup-token'
}
