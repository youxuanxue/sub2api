<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { GroupPlatform } from '@/types'
import PlatformIcon from '@/components/common/PlatformIcon.vue'
import { tkAdminGroupsPlatformTableCellClass } from '@/constants/gatewayPlatforms'

defineProps<{
  /** Group row platform (API string; includes `newapi`). */
  platform: string
}>()

const { t } = useI18n()
</script>

<template>
  <span :class="tkAdminGroupsPlatformTableCellClass(platform)">
    <PlatformIcon :platform="platform as GroupPlatform" size="xs" />
    {{ t('admin.groups.platforms.' + platform, platform) }}
  </span>
</template>
