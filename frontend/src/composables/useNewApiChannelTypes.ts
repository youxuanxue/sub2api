import { ref, shallowRef } from 'vue'
import { adminAPI } from '@/api/admin'
import type { ChannelTypeInfo } from '@/api/admin/channels'
import { unknownToErrorMessage } from '@/utils/authError'

let cache: ChannelTypeInfo[] | null = null
let inflight: Promise<ChannelTypeInfo[]> | null = null

/**
 * Cached loader for New API channel type catalog (aligned with new-api /console/channel types).
 */
export function useNewApiChannelTypes() {
  const types = shallowRef<ChannelTypeInfo[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function load(force = false) {
    if (!force && cache) {
      types.value = cache
      return
    }
    if (inflight) {
      try {
        const data = await inflight
        types.value = Array.isArray(data) ? data : []
      } catch (e) {
        error.value = unknownToErrorMessage(e, 'Failed to load channel types')
      }
      return
    }
    loading.value = true
    error.value = null
    inflight = adminAPI.channels.listChannelTypes()
    try {
      const data = await inflight
      const list = Array.isArray(data) ? data : []
      cache = list
      types.value = list
    } catch (e) {
      error.value = unknownToErrorMessage(e, 'Failed to load channel types')
      throw e
    } finally {
      inflight = null
      loading.value = false
    }
  }

  return { types, loading, error, load }
}
