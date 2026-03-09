<template>
  <section class="panel state-panel error">
    <strong>{{ title }}</strong>
    <p>{{ description }}</p>
    <button v-if="retryLabel" class="btn ghost" @click="$emit('retry')">{{ retryLabel }}</button>
  </section>
</template>

<script setup lang="ts">
defineEmits<{ retry: [] }>()

defineProps<{
  title: string
  description: string
  retryLabel?: string
}>()
</script>

<style scoped>
.state-panel {
  padding: 1rem;
}
.error {
  border-color: rgba(248, 113, 113, 0.32);
}
p {
  color: var(--muted);
}
</style>
