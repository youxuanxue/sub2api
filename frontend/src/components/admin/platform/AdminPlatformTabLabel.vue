<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { GroupPlatform } from '@/types'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { tkAdminPlatformLabelTextColor } from '@/constants/gatewayPlatforms'

withDefaults(
  defineProps<{
    platform: GroupPlatform
    iconSize?: 'xs' | 'sm' | 'md'
  }>(),
  { iconSize: 'xs' }
)

const { t } = useI18n()
</script>

<template>
  <span class="inline-flex items-center gap-1">
    <PlatformIcon :platform="platform" :size="iconSize" :class="tkAdminPlatformLabelTextColor(platform)" />
    <span :class="tkAdminPlatformLabelTextColor(platform)">{{
      t('admin.groups.platforms.' + platform, platform)
    }}</span>
  </span>
</template>
