<script setup lang="ts">
import type { GroupPlatform } from '@/types'
import { tkAdminPlatformLabelTextColor, tkAdminPlatformSoftBadgeClass } from '@/constants/gatewayPlatforms'

defineProps<{
  name: string
  platform: GroupPlatform
  rateMultiplier: number
  accountCount: number
}>()
</script>

<template>
  <span class="inline-flex flex-wrap items-center gap-1">
    <span :class="['font-medium', tkAdminPlatformLabelTextColor(platform)]">{{ name }}</span>
    <span :class="['rounded-full px-1 py-0 text-[10px]', tkAdminPlatformSoftBadgeClass(platform)]">{{ rateMultiplier }}x</span>
    <span class="text-[10px] text-gray-400">{{ accountCount }}</span>
  </span>
</template>
