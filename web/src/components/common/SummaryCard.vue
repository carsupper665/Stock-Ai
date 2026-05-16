<template>
  <article class="panel summary-card">
    <header class="summary-head">
      <span class="summary-label">{{ label }}</span>
      <slot name="badge" />
    </header>
    <strong class="summary-value">{{ value }}</strong>
    <p v-if="detail" class="summary-detail">{{ detail }}</p>
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
  padding: 1.1rem;
  border-radius: var(--radius-sm);
  background: linear-gradient(180deg, #ffffff 0%, #f8fbff 100%);
}

.summary-head {
  display: flex;
  justify-content: space-between;
  gap: 0.75rem;
  align-items: center;
  margin-bottom: 0.65rem;
}

.summary-label {
  color: var(--muted);
  font-size: 0.82rem;
  letter-spacing: 0.03em;
  text-transform: uppercase;
  font-weight: 600;
}

.summary-value {
  display: block;
  font-family: var(--font-display);
  font-size: 1.68rem;
  line-height: 1.2;
  margin-bottom: 0.35rem;
}

.summary-detail {
  margin: 0;
  color: var(--muted);
  font-size: 0.9rem;
}
</style>

