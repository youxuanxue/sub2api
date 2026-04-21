/**
 * Pure converters between Channel API shape and the per-platform form sections
 * used by ChannelsView.vue.
 *
 * Extracted from ChannelsView.vue so the round-trip can be exercised by unit
 * tests without mounting the full admin view. The previous in-component
 * implementation used a hardcoded 4-element `platformOrder` array, which
 * silently dropped any `newapi` data on the round-trip — both `apiToForm`
 * filtered keys outside the array (line 1070) and `formToAPI` then iterated
 * `form.platforms` (line 1007), so re-saving an existing channel that had
 * been linked to a `newapi` group via the API would erase its `newapi`
 * `model_mapping` and `model_pricing` rows. Driving the canonical ordering
 * from `GATEWAY_PLATFORMS` (single source of truth) closes that data-loss
 * bug as a side-effect of the contract.
 *
 * See: docs/approved/admin-ui-newapi-platform-end-to-end.md §1.5
 *      .testing/user-stories/stories/US-018-admin-ui-newapi-platform-end-to-end.md
 */

import type {
  Channel,
  ChannelModelPricing,
} from '@/api/admin/channels'
import type { PricingFormEntry } from '@/components/admin/channel/types'
import {
  apiIntervalsToForm,
  formIntervalsToAPI,
  mTokToPerToken,
  perTokenToMTok,
} from '@/components/admin/channel/types'
import { GATEWAY_PLATFORMS } from '@/constants/gatewayPlatforms'
import type { AdminGroup, GroupPlatform } from '@/types'

/** Form-level pricing rule (per-platform sub-rule rendered inside a section). */
export interface FormPricingRule {
  name: string
  group_ids: number[]
  account_ids: number[]
  pricing: PricingFormEntry[]
}

/** Per-platform UI section in the channel-edit form. */
export interface PlatformSection {
  platform: GroupPlatform
  enabled: boolean
  collapsed: boolean
  group_ids: number[]
  model_mapping: Record<string, string>
  model_pricing: PricingFormEntry[]
  web_search_emulation: boolean
  account_stats_pricing_rules: FormPricingRule[]
}

/** API payload shape that ChannelsView posts to the backend. */
export interface ChannelFormApiPayload {
  group_ids: number[]
  model_pricing: ChannelModelPricing[]
  model_mapping: Record<string, Record<string, string>>
  features_config: Record<string, unknown>
}

/**
 * Convert a {@link Channel} loaded from the backend into the per-platform
 * sections rendered by the form.
 *
 * @param channel  Channel to load — `model_mapping`, `model_pricing` and
 *                 `group_ids` are inspected to decide which platforms are
 *                 active.
 * @param allGroups  All admin groups (used to map group_id → platform).
 * @param platforms  Canonical platform order. Defaults to {@link GATEWAY_PLATFORMS}
 *                   so all five platforms (including `newapi`) are preserved.
 *                   Tests may pass a narrower list to verify drift behavior.
 */
export function apiToFormSections(
  channel: Channel,
  allGroups: AdminGroup[],
  platforms: readonly GroupPlatform[] = GATEWAY_PLATFORMS,
): PlatformSection[] {
  const groupPlatformMap = new Map<number, GroupPlatform>()
  for (const g of allGroups) {
    groupPlatformMap.set(g.id, g.platform)
  }

  const allowed = new Set<GroupPlatform>(platforms)
  const activePlatforms = new Set<GroupPlatform>()

  for (const gid of channel.group_ids || []) {
    const p = groupPlatformMap.get(gid)
    if (p && allowed.has(p)) activePlatforms.add(p)
  }
  for (const p of channel.model_pricing || []) {
    if (p.platform && allowed.has(p.platform as GroupPlatform)) {
      activePlatforms.add(p.platform as GroupPlatform)
    }
  }
  for (const p of Object.keys(channel.model_mapping || {})) {
    if (allowed.has(p as GroupPlatform)) {
      activePlatforms.add(p as GroupPlatform)
    }
  }

  const sections: PlatformSection[] = []
  for (const platform of platforms) {
    if (!activePlatforms.has(platform)) continue

    const groupIds = (channel.group_ids || []).filter(
      (gid) => groupPlatformMap.get(gid) === platform,
    )
    const mapping = (channel.model_mapping || {})[platform] || {}
    const pricing = (channel.model_pricing || [])
      .filter((p) => (p.platform || 'anthropic') === platform)
      .map(
        (p) =>
          ({
            models: p.models || [],
            billing_mode: p.billing_mode,
            input_price: perTokenToMTok(p.input_price),
            output_price: perTokenToMTok(p.output_price),
            cache_write_price: perTokenToMTok(p.cache_write_price),
            cache_read_price: perTokenToMTok(p.cache_read_price),
            image_output_price: perTokenToMTok(p.image_output_price),
            per_request_price: p.per_request_price,
            intervals: apiIntervalsToForm(p.intervals || []),
          }) as PricingFormEntry,
      )

    const fc = channel.features_config
    const wsEmulation = fc?.web_search_emulation as
      | Record<string, boolean>
      | undefined
    const webSearchEnabled = wsEmulation?.[platform] === true

    sections.push({
      platform,
      enabled: true,
      collapsed: false,
      group_ids: groupIds,
      model_mapping: { ...mapping },
      model_pricing: pricing,
      web_search_emulation: webSearchEnabled,
      account_stats_pricing_rules: [],
    })
  }

  return sections
}

/**
 * Convert per-platform form sections back to the API payload posted by
 * ChannelsView. Preserves `features_config` keys not managed by the form.
 *
 * `web_search_emulation` is always written when at least one anthropic
 * section is enabled (so the UI can flip it from on → off); cleared when
 * no anthropic section is active.
 */
export function formSectionsToApi(
  sections: PlatformSection[],
  existingFeaturesConfig?: Record<string, unknown>,
): ChannelFormApiPayload {
  const group_ids: number[] = []
  const model_pricing: ChannelModelPricing[] = []
  const model_mapping: Record<string, Record<string, string>> = {}
  const featuresConfig: Record<string, unknown> = existingFeaturesConfig
    ? { ...existingFeaturesConfig }
    : {}

  for (const section of sections) {
    if (!section.enabled) continue
    group_ids.push(...section.group_ids)

    if (Object.keys(section.model_mapping).length > 0) {
      model_mapping[section.platform] = { ...section.model_mapping }
    }

    for (const entry of section.model_pricing) {
      if (entry.models.length === 0) continue
      model_pricing.push({
        platform: section.platform,
        models: entry.models,
        billing_mode: entry.billing_mode,
        input_price: mTokToPerToken(entry.input_price),
        output_price: mTokToPerToken(entry.output_price),
        cache_write_price: mTokToPerToken(entry.cache_write_price),
        cache_read_price: mTokToPerToken(entry.cache_read_price),
        image_output_price: mTokToPerToken(entry.image_output_price),
        per_request_price:
          entry.per_request_price != null && entry.per_request_price !== ''
            ? Number(entry.per_request_price)
            : null,
        intervals: formIntervalsToAPI(entry.intervals || []),
      })
    }
  }

  // Always (re-)write web_search_emulation when an anthropic section is
  // enabled so a UI toggle from on→off correctly persists. When no anthropic
  // section is active the key is cleared to keep features_config tight.
  const wsEmulation: Record<string, boolean> = {}
  for (const section of sections) {
    if (!section.enabled) continue
    if (section.platform === 'anthropic') {
      wsEmulation[section.platform] = !!section.web_search_emulation
    }
  }
  if (Object.keys(wsEmulation).length > 0) {
    featuresConfig.web_search_emulation = wsEmulation
  } else {
    delete featuresConfig.web_search_emulation
  }

  return { group_ids, model_pricing, model_mapping, features_config: featuresConfig }
}
