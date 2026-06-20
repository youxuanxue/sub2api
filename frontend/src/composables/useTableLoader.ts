import { ref, shallowRef, triggerRef, reactive, onUnmounted, toRaw } from 'vue'
import { useDebounceFn } from '@vueuse/core'
import type { BasePaginationResponse, FetchOptions } from '@/types'
import { getPersistedPageSize, setPersistedPageSize } from './usePersistedPageSize'

interface PaginationState {
  page: number
  page_size: number
  total: number
  pages: number
}

interface TableLoaderOptions<T, P> {
  fetchFn: (page: number, pageSize: number, params: P, options?: FetchOptions) => Promise<BasePaginationResponse<T>>
  initialParams?: P
  pageSize?: number
  debounceMs?: number
}

/**
 * 通用表格数据加载 Composable
 * 统一处理分页、筛选、搜索防抖和请求取消
 */
export function useTableLoader<T, P extends Record<string, any>>(options: TableLoaderOptions<T, P>) {
  const { fetchFn, initialParams, pageSize, debounceMs = 300 } = options

  // shallowRef (not ref): list rows are display data assigned wholesale (new array identity)
  // on every load, and per-row derived data lives in id-keyed side-maps — never mutated in
  // place — so deep reactivity is pure cost (proxying ~pageSize × dozens-of-fields synchronously
  // before render). The canonical update is replace-the-array (load / setItems). If a caller
  // ever must mutate a row in place, it MUST call refreshItems() so consumers re-render.
  const items = shallowRef<T[]>([])
  const loading = ref(false)
  const params = reactive<P>({ ...(initialParams || {}) } as P)
  const pagination = reactive<PaginationState>({
    page: 1,
    page_size: pageSize ?? getPersistedPageSize(),
    total: 0,
    pages: 0
  })

  let abortController: AbortController | null = null

  const isAbortError = (error: any) => {
    return error?.name === 'AbortError' || error?.code === 'ERR_CANCELED' || error?.name === 'CanceledError'
  }

  const load = async () => {
    if (abortController) {
      abortController.abort()
    }
    const currentController = new AbortController()
    abortController = currentController
    loading.value = true

    try {
      const response = await fetchFn(
        pagination.page,
        pagination.page_size,
        toRaw(params) as P,
        { signal: currentController.signal }
      )

      items.value = response.items || []
      pagination.total = response.total || 0
      pagination.pages = response.pages || 0
    } catch (error) {
      if (!isAbortError(error)) {
        console.error('Table load error:', error)
        throw error
      }
    } finally {
      if (abortController === currentController) {
        loading.value = false
      }
    }
  }

  const reload = () => {
    pagination.page = 1
    return load()
  }

  // Canonical row update: replace the array (new identity → shallowRef reactivity fires).
  const setItems = (next: T[]) => {
    items.value = next
  }
  // Escape hatch for the rare in-place mutation of an existing row (shallowRef does not
  // track nested writes): call after mutating so consumers re-render.
  const refreshItems = () => {
    triggerRef(items)
  }

  const debouncedReload = useDebounceFn(reload, debounceMs)

  const handlePageChange = (page: number) => {
    // 确保页码在有效范围内
    const validPage = Math.max(1, Math.min(page, pagination.pages || 1))
    pagination.page = validPage
    load()
  }

  const handlePageSizeChange = (size: number) => {
    pagination.page_size = size
    pagination.page = 1
    setPersistedPageSize(size)
    load()
  }

  onUnmounted(() => {
    abortController?.abort()
  })

  return {
    items,
    loading,
    params,
    pagination,
    load,
    reload,
    setItems,
    refreshItems,
    debouncedReload,
    handlePageChange,
    handlePageSizeChange
  }
}
