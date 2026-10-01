<script setup lang="ts">
/**
 * Tall, vertically-resizable prompt composer for Studio Image / Video.
 * Default height is the SSOT — do not re-inline rows/classes in the views.
 */
withDefaults(
  defineProps<{
    modelValue: string
    placeholder: string
    disabled?: boolean
    /** Override only when a surface truly needs a different composer height. */
    rows?: number
    testId?: string
  }>(),
  { disabled: false, rows: 8, testId: undefined }
)

const emit = defineEmits<{
  (e: 'update:modelValue', value: string): void
  (e: 'input'): void
}>()

function onInput(event: Event) {
  emit('update:modelValue', (event.target as HTMLTextAreaElement).value)
  emit('input')
}
</script>

<template>
  <textarea
    :value="modelValue"
    :rows="rows"
    class="min-h-[10.5rem] w-full resize-y rounded-lg border border-gray-200 bg-white px-3 py-2.5 text-sm leading-relaxed text-gray-900 placeholder:text-gray-400 focus:border-primary-500 focus:outline-none focus:ring-2 focus:ring-primary-500/20 dark:border-dark-600 dark:bg-dark-950 dark:text-white"
    :placeholder="placeholder"
    :disabled="disabled"
    :data-testid="testId"
    @input="onInput"
  />
</template>
