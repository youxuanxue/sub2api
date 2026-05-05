<script setup lang="ts">
/**
 * ChannelTypeBadge — surfaces the upstream channel name (e.g. "Moonshot",
 * "Deepseek") for newapi (5th platform) accounts in lists. PlatformTypeBadge
 * renders the product-facing platform ("Extension Engine"); this badge sits next to it so
 * operators can tell a Moonshot account apart from a Deepseek account at a
 * glance instead of opening Edit modal to find out.
 *
 * Behavior:
 *   - Renders nothing for non-newapi platforms (kept declarative so callers
 *     don't need to v-if around it).
 *   - Renders nothing when channel_type is 0 / missing — the catalog must be
 *     loaded before a friendly label can be shown.
 *   - Falls back to "Channel #<n>" while the catalog is in flight or when
 *     the catalog returns empty (so the operator still sees *something*
 *     useful instead of a blank cell).
 *
 * The component intentionally consumes the cached useNewApiChannelTypes
 * composable so it does not refetch the catalog per row in long account
 * tables.
 */
import { computed, onMounted } from 'vue'
import { useNewApiChannelTypes } from '@/composables/useNewApiChannelTypes'
import type { AccountPlatform } from '@/types'

interface Props {
  platform: AccountPlatform | string
  channelType?: number | null
}

const props = defineProps<Props>()

const { types, load } = useNewApiChannelTypes()

onMounted(() => {
  if (props.platform === 'newapi') {
    void load().catch(() => {
      /* swallow — fallback label below covers the empty state */
    })
  }
})

const label = computed(() => {
  if (props.platform !== 'newapi') return ''
  const ct = props.channelType
  if (!ct || ct <= 0) return ''
  const found = types.value.find((c) => c.channel_type === ct)
  return found?.name || `Channel #${ct}`
})

const visible = computed(() => label.value !== '')
</script>

<template>
  <span
    v-if="visible"
    class="inline-flex items-center rounded-md bg-cyan-50 px-1.5 py-0.5 text-[10px] font-medium text-cyan-700 ring-1 ring-inset ring-cyan-200 dark:bg-cyan-900/20 dark:text-cyan-300 dark:ring-cyan-800/40"
    :title="`channel_type=${channelType}`"
  >
    {{ label }}
  </span>
</template>
