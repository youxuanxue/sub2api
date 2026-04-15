import { reactive, watch } from 'vue'
import { adminAPI } from '@/api/admin'
import type { GroupPlatform } from '@/types'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'

/** Minimal platform row shape for aggregated model suggestions (channels form). */
export interface TkChannelPlatformSectionSlice {
  platform: GroupPlatform
  enabled: boolean
  group_ids: number[]
}

/**
 * TokenKey: loads per-platform model name suggestions from aggregated group models API,
 * keyed by enabled platform sections + selected group_ids.
 */
export function useTkChannelsAggregatedSuggestions(
  getSections: () => ReadonlyArray<TkChannelPlatformSectionSlice>
) {
  const suggestionsByPlatform = reactive<Partial<Record<GroupPlatform, string[]>>>({})
  const suggestionsLoading = reactive<Partial<Record<GroupPlatform, boolean>>>({})
  const suggestionsError = reactive<Partial<Record<GroupPlatform, string | null>>>({})

  function channelMappingDatalistId(platform: GroupPlatform): string {
    return `channel-mapping-dl-${platform}`
  }

  async function refreshSuggestionsForPlatform(platform: GroupPlatform): Promise<void> {
    const section = getSections().find((s) => s.platform === platform && s.enabled)
    if (!section || section.group_ids.length === 0) {
      suggestionsByPlatform[platform] = []
      suggestionsLoading[platform] = false
      return
    }
    suggestionsLoading[platform] = true
    suggestionsError[platform] = null
    try {
      suggestionsByPlatform[platform] = await adminAPI.channels.aggregatedGroupModels({
        group_ids: section.group_ids,
        platform
      })
    } catch (e) {
      suggestionsByPlatform[platform] = []
      suggestionsError[platform] = e instanceof Error ? e.message : 'failed to load suggestions'
    } finally {
      suggestionsLoading[platform] = false
    }
  }

  watch(
    () =>
      getSections().map((s) => ({
        platform: s.platform,
        enabled: s.enabled,
        groups: [...s.group_ids].slice().sort((a, b) => a - b).join(',')
      })),
    (rows) => {
      for (const row of rows) {
        if (!row.enabled) {
          suggestionsByPlatform[row.platform] = []
          suggestionsLoading[row.platform] = false
          continue
        }
        void refreshSuggestionsForPlatform(row.platform)
      }
    },
    { deep: true }
  )

  function clearSuggestionsCache(): void {
    for (const p of GATEWAY_PLATFORMS) {
      delete suggestionsByPlatform[p as GroupPlatform]
      delete suggestionsLoading[p as GroupPlatform]
      delete suggestionsError[p as GroupPlatform]
    }
  }

  return {
    suggestionsByPlatform,
    suggestionsLoading,
    suggestionsError,
    channelMappingDatalistId,
    refreshSuggestionsForPlatform,
    clearSuggestionsCache
  }
}
