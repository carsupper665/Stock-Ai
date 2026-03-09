<template>
  <section class="panel timeline">
    <div class="timeline-head">
      <div>
        <strong>Replay Timeline</strong>
        <p>Seek within the dataset range and apply immediately.</p>
      </div>
      <slot name="status" />
    </div>
    <div class="timeline-grid">
      <label>
        Current time
        <input class="control" type="datetime-local" :disabled="pending || readonly" :value="inputValue" @input="onInput" />
      </label>
      <label>
        Relative position
        <input class="range" type="range" min="0" max="1000" :disabled="pending || readonly" :value="sliderValue" @input="onSlider" />
      </label>
      <button class="btn primary" :disabled="pending || readonly" @click="emit('seek', draftValue)">
        {{ pending ? 'Seeking…' : readonly ? 'Read only' : 'Seek replay time' }}
      </button>
    </div>
  </section>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'

const props = withDefaults(defineProps<{
  currentTime?: string | null
  minTime?: string | null
  maxTime?: string | null
  pending?: boolean
  readonly?: boolean
}>(), {
  currentTime: null,
  minTime: null,
  maxTime: null,
  pending: false,
  readonly: false,
})

const emit = defineEmits<{ seek: [string] }>()

const draftValue = ref(props.currentTime ?? '')

watch(() => props.currentTime, (value) => {
  draftValue.value = value ?? ''
})

const inputValue = computed(() => {
  if (!draftValue.value) return ''
  const date = new Date(draftValue.value)
  if (Number.isNaN(date.getTime())) return ''
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000)
  return local.toISOString().slice(0, 16)
})

const sliderValue = computed(() => {
  if (!props.minTime || !props.maxTime || !draftValue.value) return 0
  const min = new Date(props.minTime).getTime()
  const max = new Date(props.maxTime).getTime()
  const current = new Date(draftValue.value).getTime()
  if (max <= min) return 0
  return Math.min(1000, Math.max(0, Math.round(((current - min) / (max - min)) * 1000)))
})

function onInput(event: Event) {
  const target = event.target as HTMLInputElement
  const local = new Date(target.value)
  draftValue.value = new Date(local.getTime() + local.getTimezoneOffset() * 60000).toISOString()
}

function onSlider(event: Event) {
  if (!props.minTime || !props.maxTime) return
  const target = event.target as HTMLInputElement
  const min = new Date(props.minTime).getTime()
  const max = new Date(props.maxTime).getTime()
  const percent = Number(target.value) / 1000
  draftValue.value = new Date(min + (max - min) * percent).toISOString()
}
</script>

<style scoped>
.timeline {
  padding: 1rem;
}
.timeline-head {
  display: flex;
  justify-content: space-between;
  gap: 1rem;
  margin-bottom: 1rem;
}
.timeline-head p {
  margin: 0.35rem 0 0;
  color: var(--muted);
}
.timeline-grid {
  display: grid;
  grid-template-columns: 1.3fr 1fr auto;
  gap: 1rem;
  align-items: end;
}
.range {
  width: 100%;
}
@media (max-width: 960px) {
  .timeline-grid {
    grid-template-columns: 1fr;
  }
}
</style>
