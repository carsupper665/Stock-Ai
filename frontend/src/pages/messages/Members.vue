<script setup>
import { reactive } from 'vue'
import ChatPermission from '../../shared/ChatPermission.vue'
import { updateAccount } from '../accounts/api.js'
import { say } from '../../shared/toast.js'
import { confirm } from '../../shared/confirm.js'
import { muteChat, unmuteChat } from './api.js'

const props = defineProps({ accounts: Array, now: Number })
const emit = defineEmits(['changed'])
const drafts = reactive({})
const saving = reactive({})
const key = (account, field) => `${account.id}:${field}`
const remaining = (account) => Math.max(0, Math.ceil((Date.parse(account.chat_muted_until) - props.now) / 60000)) || 0
async function mute(account) {
  const values = await confirm({ title: `禁言 ${account.user_name}`, ok: '禁言', fields: [
    { name: 'minutes', label: '分鐘數（1～43200）', type: 'number', min: 1, max: 43200, step: 1 },
    { name: 'reason', label: '禁言原因（最多 200 字）', maxLength: 200 },
  ] })
  if (!values || saving[account.id]) return
  saving[account.id] = true
  try { emit('changed', await muteChat(account.id, { minutes: Number(values.minutes), reason: values.reason })) }
  catch (e) { say(e.message, 'neg') }
  finally { saving[account.id] = false }
}
async function unmute(account) {
  if (saving[account.id]) return
  saving[account.id] = true
  try { emit('changed', await unmuteChat(account.id)) }
  catch (e) { say(e.message, 'neg') }
  finally { saving[account.id] = false }
}
async function saveRate(account, field, event) {
  const value = Number(event.target.value)
  if (saving[account.id] || value === account[field]) { delete drafts[key(account, field)]; return }
  saving[account.id] = true
  try {
    const saved = await updateAccount(account.id, { [field]: value })
    emit('changed', saved)
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    delete drafts[key(account, field)]
    event.target.value = String(props.accounts.find((item) => item.id === account.id)?.[field] ?? account[field])
    saving[account.id] = false
  }
}
</script>

<template>
  <details class="card members" open>
    <summary>成員管理</summary>
    <div v-for="account in accounts" :key="account.id" class="member col">
      <div class="row member-name"><b>{{ account.user_name }}</b><span class="tag outline">{{ account.status }}</span></div>
      <ChatPermission :account="account" @changed="emit('changed', $event)" />
      <div class="row mute-state">
        <template v-if="remaining(account)">
          <span>禁言中 · 剩 {{ remaining(account) }} 分鐘 · {{ account.chat_mute_reason }}</span>
          <button :disabled="saving[account.id]" @click="unmute(account)">解除</button>
        </template>
        <button v-else :disabled="saving[account.id]" @click="mute(account)">禁言</button>
      </div>
      <label class="row rate">每
        <input :value="drafts[key(account, 'chat_rate_minutes')] ?? account.chat_rate_minutes" type="number" min="1" max="1440" step="1" :aria-label="`${account.user_name} 頻率分鐘`" :disabled="saving[account.id]" @input="drafts[key(account, 'chat_rate_minutes')] = $event.target.value" @blur="saveRate(account, 'chat_rate_minutes', $event)" @keydown.enter.prevent="$event.target.blur()" />
        分鐘最多
        <input :value="drafts[key(account, 'chat_rate_count')] ?? account.chat_rate_count" type="number" min="1" max="100" step="1" :aria-label="`${account.user_name} 頻率則數`" :disabled="saving[account.id]" @input="drafts[key(account, 'chat_rate_count')] = $event.target.value" @blur="saveRate(account, 'chat_rate_count', $event)" @keydown.enter.prevent="$event.target.blur()" />則
      </label>
    </div>
    <p v-if="!accounts.length" class="note">尚無交易帳號。</p>
  </details>
</template>

<style scoped>
.members { min-width: 0; align-self: start; }
summary { cursor: pointer; font-weight: 600; }
.member { gap: 10px; padding-top: 14px; margin-top: 14px; border-top: 1px solid var(--line); min-width: 0; }
.member-name { flex-wrap: wrap; overflow-wrap: anywhere; }
.member-name b { min-width: 0; }
.member :deep(.row) { flex-wrap: wrap; }
.member :deep(select) { max-width: 100%; }
.rate { gap: 5px; }
.rate input { width: 64px; min-width: 0; }
.mute-state { overflow-wrap: anywhere; }
</style>
