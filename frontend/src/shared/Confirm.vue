<script setup>
import { computed, ref, watch } from 'vue'

import { dialog, settle } from './confirm.js'

const el = ref(null)
const valid = computed(() => dialog.fields.every((field) => {
  const value = String(dialog.values[field.name] ?? '')
  if (!value.trim() || (field.maxLength && Array.from(value).length > field.maxLength)) return false
  if (field.type !== 'number') return true
  const number = Number(value)
  return Number.isFinite(number) && (field.step !== 1 || Number.isInteger(number)) && (field.min == null || number >= field.min) && (field.max == null || number <= field.max)
}))

watch(
  () => dialog.open,
  (open) => (open ? el.value.showModal() : el.value.close()),
)

function submit() {
  if (!valid.value) return
  if (dialog.fields.length) settle({ ...dialog.values })
  else if (dialog.input) settle(dialog.value)
  else settle(true)
}
</script>

<template>
  <dialog ref="el" :data-tone="dialog.tone" @cancel.prevent="settle(null)">
    <form method="dialog" @submit.prevent="submit">
      <h2>{{ dialog.title }}</h2>
      <ul>
        <li v-for="line in dialog.lines" :key="line"><span :data-tone="dialog.tone">—</span> {{ line }}</li>
      </ul>
      <label v-if="dialog.input">
        {{ dialog.input.label }}
        <input v-model="dialog.value" :class="{ mono: dialog.input.mono }" :placeholder="dialog.input.placeholder" :type="dialog.input.type ?? 'text'" autofocus autocomplete="off" required />
      </label>
      <label v-for="field in dialog.fields" :key="field.name">
        {{ field.label }}
        <input v-model="dialog.values[field.name]" :type="field.type ?? 'text'" :min="field.min" :max="field.max" :step="field.step" :aria-label="field.label" autocomplete="off" required />
      </label>
      <menu>
        <button type="button" @click="settle(null)">取消</button>
        <button :data-tone="dialog.tone" :disabled="!valid">{{ dialog.ok }}</button>
      </menu>
    </form>
  </dialog>
</template>
