<template>
  <article class="panel summary-card">
    <header>
      <span>{{ label }}</span>
      <slot name="badge" />
    </header>
    <strong>{{ value }}</strong>
    <p>{{ detail }}</p>
  </article>
</template>

<script setup lang="ts">
defineProps<{
  label: string
  value: string | number
  detail?: string
}>()
</script>

<style scoped>
.summary-card {
  padding: 1rem;
}
header {
  display: flex;
  justify-content: space-between;
  color: var(--muted);
  margin-bottom: 0.75rem;
}
strong {
  display: block;
  font-family: var(--font-display);
  font-size: 1.7rem;
  margin-bottom: 0.35rem;
}
p {
  margin: 0;
  color: var(--muted);
}
</style>
