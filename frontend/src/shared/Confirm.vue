<script setup>
import { ref, watch } from 'vue'

import { dialog, settle } from './confirm.js'

const el = ref(null)

watch(
  () => dialog.open,
  (open) => (open ? el.value.showModal() : el.value.close()),
)

function submit() {
  if (dialog.input) settle(dialog.value)
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
      <menu>
        <button type="button" @click="settle(null)">取消</button>
        <button :data-tone="dialog.tone">{{ dialog.ok }}</button>
      </menu>
    </form>
  </dialog>
</template>
