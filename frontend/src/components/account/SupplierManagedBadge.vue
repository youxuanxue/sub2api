<template>
  <a
    v-if="info.managed"
    data-testid="supplier-managed-badge"
    :href="info.href"
    :title="viewHint"
    class="inline-flex w-fit items-center rounded-md border border-amber-300 bg-amber-50 px-2 py-0.5 text-[11px] font-semibold text-amber-800 transition-colors hover:border-amber-400 hover:bg-amber-100 dark:border-amber-700/70 dark:bg-amber-950/30 dark:text-amber-200 dark:hover:bg-amber-900/40"
  >
    {{ info.label }}
  </a>
</template>

<script setup lang="ts">
import { computed, watch } from 'vue'

import {
  useSupplierManagedAccount,
  type SupplierManagedAccountLike
} from '@/composables/useSupplierManagedAccount'

const props = defineProps<{
  account: SupplierManagedAccountLike | null
}>()

const { inspect, viewHint, ensureSourceDirectoryLoaded } = useSupplierManagedAccount()
const info = computed(() => inspect(props.account))

watch(
  () => info.value.sourceId,
  (sourceID) => {
    if (info.value.managed && sourceID !== null) {
      void ensureSourceDirectoryLoaded().catch(() => undefined)
    }
  },
  { immediate: true }
)
</script>
