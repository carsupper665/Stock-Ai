<template>
  <section class="panel state-panel">
    <strong>{{ title }}</strong>
    <p>{{ description }}</p>
    <slot />
  </section>
</template>

<script setup lang="ts">
defineProps<{
  title: string
  description: string
}>()
</script>

<style scoped>
.state-panel {
  padding: 1rem;
  text-align: left;
}
p {
  color: var(--muted);
}
</style>
