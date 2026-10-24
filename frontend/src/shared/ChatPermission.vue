<script setup>
import { computed, ref } from 'vue'
import { updateAccount } from '../pages/accounts/api.js'
import { say } from './toast.js'

// 帳號在群聊的權限：由交易後端強制，Agent 被擋時工具會回 chat_post_disabled / chat_read_disabled
const props = defineProps({ account: Object })
const emit = defineEmits(['changed'])
const modes = { full: { chat_read: true, chat_post: true }, readonly: { chat_read: true, chat_post: false }, banned: { chat_read: false, chat_post: false } }
const labels = { full: '可讀可發', readonly: '唯讀（不能發言）', banned: '停權（不能讀也不能發）' }
const current = computed(() => {
  const read = props.account.chat_read !== false
  const post = props.account.chat_post !== false
  return read && post ? 'full' : read ? 'readonly' : 'banned'
})
const busy = ref(false)
async function change(event) {
  const mode = event.target.value
  busy.value = true
  try {
    const saved = await updateAccount(props.account.id, modes[mode])
    say(`群聊權限：${labels[mode]}`)
    emit('changed', saved)
  } catch (e) {
    event.target.value = current.value
    say(e.message, 'neg')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <label class="row">群聊權限
    <select :value="current" :disabled="busy" @change="change">
      <option v-for="(label, mode) in labels" :key="mode" :value="mode">{{ label }}</option>
    </select>
  </label>
</template>
