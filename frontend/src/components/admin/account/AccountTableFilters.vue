<template>
  <div class="min-w-0 w-full">
    <div class="flex flex-wrap items-center gap-2 xl:flex-nowrap">
      <SearchInput
        :model-value="searchQuery"
        :placeholder="t('admin.accounts.searchAccounts')"
        class="min-w-[14rem] flex-[1.5]"
        @update:model-value="$emit('update:searchQuery', $event)"
        @search="$emit('change')"
      />
      <Select :model-value="filters.platform" :aria-label="t('admin.accounts.allPlatforms')" class="min-w-[8rem] flex-1" :options="pOpts" @update:model-value="updatePlatform" @change="$emit('change')" />
      <Select :model-value="filters.status" :aria-label="t('admin.accounts.allStatus')" class="min-w-[8rem] flex-1" :options="sOpts" @update:model-value="updateStatus" @change="$emit('change')" />
      <Select
        v-if="isNewApiPlatform"
        :model-value="channelTypeFilterValue"
        :aria-label="t('admin.accounts.allChannelTypes')"
        class="min-w-[8rem] flex-1"
        :options="channelTypeOpts"
        @update:model-value="updateChannelType"
        @change="$emit('change')"
      />
      <Select
        :model-value="filters.proxy_name || ''"
        :aria-label="t('admin.accounts.allProxies')"
        class="min-w-[8rem] flex-1"
        :options="proxyOpts"
        searchable
        @update:model-value="updateProxyName"
        @change="$emit('change')"
      />
      <Select :model-value="filters.type" :aria-label="t('admin.accounts.allTypes')" class="min-w-[8rem] flex-1" :options="tOpts" @update:model-value="updateType" @change="$emit('change')" />
      <Select :model-value="filters.privacy_mode" :aria-label="t('admin.accounts.allPrivacyModes')" class="min-w-[8rem] flex-1" :options="privacyOpts" @update:model-value="updatePrivacyMode" @change="$emit('change')" />
      <Select :model-value="filters.group" :aria-label="t('admin.accounts.allGroups')" class="min-w-[8rem] flex-1" :options="gOpts" @update:model-value="updateGroup" @change="$emit('change')" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'; import Select from '@/components/common/Select.vue'; import SearchInput from '@/components/common/SearchInput.vue'
import { usePlatformOptions } from '@/composables/usePlatformOptions'
import { useNewApiChannelTypes } from '@/composables/useNewApiChannelTypes'
import { PLATFORM_NEWAPI } from '@/constants/gatewayPlatforms'
import type { AdminGroup, Proxy as AccountProxy } from '@/types'
const props = defineProps<{ searchQuery: string; filters: Record<string, any>; groups?: AdminGroup[]; proxies?: AccountProxy[] }>()
const emit = defineEmits(['update:searchQuery', 'update:filters', 'change']); const { t } = useI18n()
const isNewApiPlatform = computed(() => props.filters.platform === PLATFORM_NEWAPI)
const channelTypeFilterValue = computed(() => {
  const raw = props.filters.channel_type
  if (raw === undefined || raw === null || raw === 0) return ''
  return String(raw)
})
const updatePlatform = (value: string | number | boolean | null) => {
  emit('update:filters', {
    ...props.filters,
    platform: value,
    ...(value === PLATFORM_NEWAPI ? {} : { channel_type: '' })
  })
}
const updateChannelType = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, channel_type: value == null ? '' : String(value) })
}
const updateType = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, type: value }) }
const updateStatus = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, status: value }) }
const updatePrivacyMode = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, privacy_mode: value }) }
const updateProxyName = (value: string | number | boolean | null) => {
  emit('update:filters', { ...props.filters, proxy_name: value ?? '' })
}
const updateGroup = (value: string | number | boolean | null) => { emit('update:filters', { ...props.filters, group: value }) }
const { optionsWithAll } = usePlatformOptions()
const basePlatformOptions = optionsWithAll(() => t('admin.accounts.allPlatforms'))
const pOpts = computed(() =>
  basePlatformOptions.value.filter((option) => option.value !== 'composite'),
)
const { types: channelTypes, load: loadChannelTypes } = useNewApiChannelTypes()
watch(isNewApiPlatform, (selected) => {
  if (selected) {
    void loadChannelTypes().catch(() => {
      /* catalog fallback labels still work when empty */
    })
  }
}, { immediate: true })
const channelTypeOpts = computed(() => [
  { value: '', label: t('admin.accounts.allChannelTypes') },
  ...channelTypes.value.map((item) => ({
    value: String(item.channel_type),
    label: item.name || `Channel #${item.channel_type}`
  }))
])
const tOpts = computed(() => [{ value: '', label: t('admin.accounts.allTypes') }, { value: 'oauth', label: t('admin.accounts.oauthType') }, { value: 'setup-token', label: t('admin.accounts.setupToken') }, { value: 'apikey', label: t('admin.accounts.apiKey') }, { value: 'bedrock', label: 'AWS Bedrock' }])
const sOpts = computed(() => [{ value: '', label: t('admin.accounts.allStatus') }, { value: 'active', label: t('admin.accounts.status.active') }, { value: 'inactive', label: t('admin.accounts.status.inactive') }, { value: 'error', label: t('admin.accounts.status.error') }, { value: 'rate_limited', label: t('admin.accounts.status.rateLimited') }, { value: 'temp_unschedulable', label: t('admin.accounts.status.tempUnschedulable') }, { value: 'unschedulable', label: t('admin.accounts.status.unschedulable') }])
const privacyOpts = computed(() => [
  { value: '', label: t('admin.accounts.allPrivacyModes') },
  { value: '__unset__', label: t('admin.accounts.privacyUnset') },
  { value: 'training_off', label: 'Privacy' },
  { value: 'training_set_cf_blocked', label: 'CF' },
  { value: 'training_set_failed', label: 'Fail' }
])
const proxyOpts = computed(() => [
  { value: '', label: t('admin.accounts.allProxies') },
  ...Array.from(new Set((props.proxies || []).map(proxy => proxy.name))).map(name => ({ value: name, label: name }))
])
const gOpts = computed(() => [
  { value: '', label: t('admin.accounts.allGroups') },
  { value: 'ungrouped', label: t('admin.accounts.ungroupedGroup') },
  ...(props.groups || []).map(g => ({ value: String(g.id), label: g.name }))
])
</script>
