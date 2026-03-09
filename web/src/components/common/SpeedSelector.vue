<template>
  <div class="speed-selector">
    <button
      v-for="option in options"
      :key="option"
      class="btn"
      :class="{ primary: modelValue === option }"
      :disabled="pending || readonly"
      @click="$emit('update:modelValue', option)"
    >
      {{ option }}x
    </button>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{
  modelValue: number
  options?: number[]
  pending?: boolean
  readonly?: boolean
}>(), {
  options: () => [0.5, 1, 2, 5, 10],
  pending: false,
  readonly: false,
})

defineEmits<{ 'update:modelValue': [number] }>()
</script>

<style scoped>
.speed-selector {
  display: flex;
  flex-wrap: wrap;
  gap: 0.6rem;
}
</style>
