<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { catalogAudioPrice, formatCatalogMediaPrice } from '@/utils/pricingCatalogPresentation.tk'

const props = defineProps<{ mode?: string; perCharacter?: number | null; perInputSecond?: number | null }>()
const { t } = useI18n()
const price = computed(() => catalogAudioPrice(props.mode, props.perCharacter, props.perInputSecond))
</script>

<template>
  <span v-if="price" class="inline-flex flex-wrap items-baseline gap-1 tabular-nums" data-tk="catalog-audio-price">
    <span>{{ formatCatalogMediaPrice(price.value) }}</span>
    <span class="text-xs font-normal text-gray-400">{{ t(price.unit) }}</span>
  </span>
</template>
