/**
 * Channel type ids that support "fetch model list" from upstream (GET /v1/models or provider-specific),
 * aligned with new-api `web/src/constants/channel.constants.js` MODEL_FETCHABLE_CHANNEL_TYPES.
 */
export const NEW_API_UPSTREAM_FETCHABLE_CHANNEL_TYPES = new Set<number>([
  1, 4, 14, 34, 17, 26, 27, 24, 47, 25, 20, 23, 31, 40, 42, 48, 43, 45
])

export function isNewApiUpstreamFetchableChannelType(channelType: number): boolean {
  return NEW_API_UPSTREAM_FETCHABLE_CHANNEL_TYPES.has(channelType)
}
