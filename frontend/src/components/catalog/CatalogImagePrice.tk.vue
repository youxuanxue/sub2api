<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import CatalogTieredPriceGrid from './CatalogTieredPriceGrid.tk.vue'
import {
  catalogImagePrice,
  formatCatalogMediaPrice,
  formatCatalogTokenPrice,
} from '@/utils/pricingCatalogPresentation.tk'

const props = defineProps<{
  outputCostPerImage?: number | null
  outputCostPerImageToken?: number | null
  inputCostPerImageToken?: number | null
  inputPer1kTokens?: number | null
}>()

const { t } = useI18n()

const view = computed(() =>
  catalogImagePrice({
    outputCostPerImage: props.outputCostPerImage,
    outputCostPerImageToken: props.outputCostPerImageToken,
    inputCostPerImageToken: props.inputCostPerImageToken,
    inputPer1kTokens: props.inputPer1kTokens,
  })
)

const tokenLines = computed(() => {
  if (view.value.kind !== 'image_tokens') return []
  return view.value.lines.map((line) => ({
    label: t(line.labelKey),
    priceText: formatCatalogTokenPrice(line.per1k),
  }))
})
</script>

<template>
  <template v-if="view.kind === 'per_image'">
    <div class="min-w-0" data-tk="catalog-image-price-per-image">
      <span class="text-[10px] uppercase tracking-wider text-gray-400 dark:text-dark-500">{{
        t('models.outputPrice')
      }}</span>
      <p class="flex flex-wrap items-baseline gap-1 text-sm font-semibold text-gray-900 dark:text-white">
        {{ formatCatalogMediaPrice(view.price) }}
        <span class="text-[10px] font-normal text-gray-400 dark:text-dark-500">{{
          t('pricing.perImage')
        }}</span>
      </p>
    </div>
  </template>
  <CatalogTieredPriceGrid
    v-else-if="view.kind === 'image_tokens'"
    mode="single"
    :lines="tokenLines"
    price-label=""
    :unit-label="t('pricing.perMillionTokens')"
    data-tk="catalog-image-price-tokens"
  />
  <div v-else class="min-w-0" data-tk="catalog-image-price-missing">
    <span class="text-[10px] uppercase tracking-wider text-gray-400 dark:text-dark-500">{{
      t('models.outputPrice')
    }}</span>
    <p class="flex flex-wrap items-baseline gap-1 text-sm font-semibold text-gray-900 dark:text-white">
      —
      <span class="text-[10px] font-normal text-gray-400 dark:text-dark-500">{{
        t('pricing.perImage')
      }}</span>
    </p>
  </div>
</template>
