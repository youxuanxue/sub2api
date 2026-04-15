import { shallowRef, ref, type ShallowRef, type Ref } from 'vue'
import { adminAPI } from '@/api/admin'

let cache: Record<string, string[]> | null = null
let inflight: Promise<Record<string, string[]>> | null = null

/**
 * Cached loader for New API default models per channel type (adaptor GetModelList),
 * aligned with new-api GET /api/models and "填入相关模型".
 */
export function useNewApiChannelTypeModels(): {
  map: ShallowRef<Record<string, string[]>>
  error: Ref<string | null>
  load: (force?: boolean) => Promise<void>
} {
  const map = shallowRef<Record<string, string[]>>(cache ?? {})
  const error = ref<string | null>(null)

  async function load(force = false) {
    if (!force && cache) {
      map.value = cache
      error.value = null
      return
    }
    if (inflight) {
      try {
        await inflight
        map.value = cache ?? {}
        error.value = null
      } catch {
        // keep previous map
      }
      return
    }
    inflight = adminAPI.channels.listChannelTypeModels()
    try {
      const data = await inflight
      cache = data
      map.value = data
      error.value = null
    } catch (e) {
      error.value = e instanceof Error ? e.message : 'failed to load channel type models'
    } finally {
      inflight = null
    }
  }

  return { map, error, load }
}
