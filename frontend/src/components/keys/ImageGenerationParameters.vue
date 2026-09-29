<template>
  <div class="space-y-3" data-testid="image-generation-parameters">
    <div v-if="plan.sizes.length">
      <div class="mb-1.5 text-xs font-semibold text-gray-500">{{ t('studio.image.aspectLabel') }}</div>
      <div class="flex flex-wrap gap-2">
        <button v-for="option in plan.sizes" :key="option.ratio" type="button" :disabled="disabled"
          :data-testid="aspectTestId" :aria-pressed="modelValue.ratio === option.ratio"
          class="rounded-lg border px-3 py-1.5 text-sm disabled:opacity-50"
          :class="modelValue.ratio === option.ratio ? 'border-primary-600 bg-primary-600 text-white' : 'border-gray-300 dark:border-dark-600'"
          @click="update({ ratio: option.ratio })">
          {{ option.ratio }}
          <span v-if="option.value !== option.ratio" class="block text-[10px] opacity-70">{{ option.value.replace('x', '×') }}</span>
        </button>
      </div>
    </div>
    <p v-if="plan.softAspectRatio" class="text-xs text-gray-500" data-testid="image-soft-ratio">{{ t('imageGeneration.softRatio') }}</p>
    <label v-if="plan.counts.length > 1" class="flex items-center gap-3 text-sm">
      {{ t('studio.image.count') }}
      <select :value="modelValue.n" :disabled="disabled" data-testid="image-generation-count" class="rounded border bg-white px-2 py-1 dark:bg-dark-900" @change="update({ n: Number(($event.target as HTMLSelectElement).value) })">
        <option v-for="count in plan.counts" :key="count" :value="count">{{ count }}</option>
      </select>
    </label>
  </div>
</template>
<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { normalizeImageOptions, type ImageGenerationOptions, type ImageGenerationPlan } from '@/utils/imageGeneration.tk'
const props = withDefaults(defineProps<{ plan: ImageGenerationPlan; modelValue: ImageGenerationOptions; disabled?: boolean; aspectTestId?: string }>(), { aspectTestId: 'image-generation-aspect' })
const emit = defineEmits<{ 'update:modelValue': [ImageGenerationOptions] }>()
const { t } = useI18n()
function update(delta: Partial<ImageGenerationOptions>) { emit('update:modelValue', normalizeImageOptions(props.plan, { ...props.modelValue, ...delta })) }
</script>
