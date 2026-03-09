<template>
  <article class="panel event-item">
    <header>
      <strong>{{ title }}</strong>
      <small>{{ event.created_at }}</small>
    </header>
    <p>{{ description }}</p>
  </article>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import type { DomainEvent } from '@/types/domain'

const props = defineProps<{ event: DomainEvent }>()

const title = computed(() => props.event.topic)
const description = computed(() => JSON.stringify(props.event.payload ?? {}, null, 0) || 'No payload')
</script>

<style scoped>
.event-item {
  padding: 0.9rem 1rem;
}
header {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
}
p {
  margin: 0.5rem 0 0;
  color: var(--muted);
  word-break: break-word;
}
small {
  color: var(--muted);
}
</style>
