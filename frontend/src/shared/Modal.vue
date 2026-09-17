<script setup>
import { onMounted, ref } from 'vue'

// 表單用的 modal：用 v-if 掛上就開，Esc／取消發 close 由父層拆掉
defineProps({ tone: { type: String, default: 'acc' } })
defineEmits(['close'])
const el = ref(null)
onMounted(() => el.value.showModal())
</script>

<template>
  <dialog ref="el" :data-tone="tone" @cancel.prevent="$emit('close')">
    <slot />
  </dialog>
</template>
