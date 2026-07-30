<template>
  <div class="w-full min-w-0" data-tk="catalog-tiered-price-grid">
    <div
      class="grid items-baseline gap-x-4 gap-y-0.5"
      :class="gridColsClass"
    >
      <!-- Header: unit shares the tier column; input/output stay aligned. -->
      <span
        v-if="mode === 'token'"
        class="self-end text-[10px] leading-none text-gray-400 dark:text-dark-500"
        data-tk="catalog-tier-unit"
      >
        {{ unitLabel }}
      </span>
      <span
        v-if="mode === 'token'"
        class="text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 dark:text-dark-500"
      >
        {{ inputLabel }}
      </span>
      <span
        v-if="mode === 'token'"
        class="text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 dark:text-dark-500"
      >
        {{ outputLabel }}
      </span>

      <span
        v-if="mode === 'single'"
        class="self-end text-[10px] leading-none text-gray-400 dark:text-dark-500"
        data-tk="catalog-tier-unit"
      >
        {{ unitLabel }}
      </span>
      <span
        v-if="mode === 'single'"
        class="text-right text-[10px] font-medium uppercase tracking-wider text-gray-400 dark:text-dark-500"
      >
        {{ priceLabel }}
      </span>

      <template v-for="(line, idx) in lines" :key="lineKey(line, idx)">
        <span
          class="shrink-0 text-[10px] font-normal tabular-nums text-gray-400 dark:text-dark-500"
          data-tk="catalog-tier-label"
        >
          {{ line.label }}
        </span>
        <template v-if="mode === 'token'">
          <span
            class="text-right text-sm font-semibold tabular-nums text-gray-900 dark:text-white"
            data-tk="catalog-tier-input"
          >
            {{ line.inputText ?? '—' }}
          </span>
          <span
            class="text-right text-sm font-semibold tabular-nums text-gray-900 dark:text-white"
            data-tk="catalog-tier-output"
          >
            {{ line.outputText ?? '—' }}
          </span>
        </template>
        <span
          v-else
          class="text-right text-sm font-semibold tabular-nums text-gray-900 dark:text-white"
          data-tk="catalog-tier-price"
        >
          {{ line.priceText ?? '—' }}
        </span>
      </template>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'

export interface CatalogTieredPriceLine {
  /** Empty for flat (single-price) rows. */
  label: string
  inputText?: string | null
  outputText?: string | null
  priceText?: string | null
}

const props = withDefaults(
  defineProps<{
    lines: CatalogTieredPriceLine[]
    mode?: 'token' | 'single'
    inputLabel?: string
    outputLabel?: string
    priceLabel?: string
    unitLabel?: string
  }>(),
  {
    mode: 'token',
    inputLabel: '',
    outputLabel: '',
    priceLabel: '',
    unitLabel: '',
  },
)

const gridColsClass = computed(() =>
  props.mode === 'token'
    ? 'grid-cols-[minmax(4.5rem,max-content)_minmax(0,1fr)_minmax(0,1fr)]'
    : 'grid-cols-[minmax(4.5rem,max-content)_minmax(0,1fr)]',
)

function lineKey(line: CatalogTieredPriceLine, idx: number): string {
  return line.label.trim() ? line.label : `flat-${idx}`
}
</script>
