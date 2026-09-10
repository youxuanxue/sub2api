<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import CatalogTieredPriceGrid from './CatalogTieredPriceGrid.tk.vue'
import { formatCatalogTokenPrice } from '@/utils/pricingCatalogPresentation.tk'

const props = defineProps<{ inputPer1k?: number | null; perImageInputToken: number }>()
const { t } = useI18n()
const lines = computed(() => [
  { label: t('pricing.modality.text'), priceText: props.inputPer1k == null ? undefined : formatCatalogTokenPrice(props.inputPer1k) },
  { label: t('pricing.modality.image'), priceText: formatCatalogTokenPrice(props.perImageInputToken * 1000) },
])
</script>

<template>
  <CatalogTieredPriceGrid
    mode="single"
    :lines="lines"
    :price-label="t('models.inputPrice')"
    :unit-label="t('pricing.perMillionTokens')"
    data-tk-embedding-price
  />
</template>
