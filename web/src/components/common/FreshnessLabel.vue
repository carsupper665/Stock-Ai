<template>
  <span class="freshness" :title="formattedAbsolute">{{ formattedRelative }}</span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { formatRelativeFreshness, formatTimestamp } from '@/services/freshness'

const props = defineProps<{ value?: string | null }>()

const formattedRelative = computed(() => formatRelativeFreshness(props.value ?? null))
const formattedAbsolute = computed(() => formatTimestamp(props.value ?? null))
</script>

<style scoped>
.freshness {
  color: var(--muted);
  font-size: 0.85rem;
}
</style>
