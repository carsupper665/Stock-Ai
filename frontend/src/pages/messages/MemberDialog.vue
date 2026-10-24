<script setup>
import { computed, reactive, ref } from 'vue'

import Avatar from '../../shared/Avatar.vue'
import ChatPermission from '../../shared/ChatPermission.vue'
import Modal from '../../shared/Modal.vue'
import { fmtTime } from '../../shared/format.js'
import { say } from '../../shared/toast.js'
import { updateAccount } from '../accounts/api.js'
import { muteChat, unmuteChat } from './api.js'

const props = defineProps({ member: Object })
const emit = defineEmits(['changed', 'mention', 'close'])

const account = computed(() => props.member.account)
const sessionLabels = { active: 'running · 正在執行 Run', waiting: 'running · 等待下一個 Run', stopped: 'stopped' }
const mutedUntil = computed(() => {
  const until = Date.parse(account.value.chat_muted_until)
  return until > Date.now() ? until : null
})
const remaining = computed(() => Math.ceil((mutedUntil.value - Date.now()) / 60000))

const busy = ref(false)
const mute = reactive({ minutes: '', reason: '' })
const muteValid = computed(() => {
  const minutes = Number(mute.minutes)
  const reason = mute.reason.trim()
  return Number.isInteger(minutes) && minutes >= 1 && minutes <= 43200 && reason !== '' && Array.from(reason).length <= 200
})

async function run(request) {
  busy.value = true
  try {
    emit('changed', await request())
    return true
  } catch (e) {
    say(e.message, 'neg')
    return false
  } finally {
    busy.value = false
  }
}

async function submitMute() {
  if (!muteValid.value || busy.value) return
  if (await run(() => muteChat(account.value.id, { minutes: Number(mute.minutes), reason: mute.reason.trim() }))) Object.assign(mute, { minutes: '', reason: '' })
}
const submitUnmute = () => run(() => unmuteChat(account.value.id))

// 頻率：失焦或 Enter 才送出，只送改動的欄位；失敗時欄位還原成目前的值。
async function saveRate(field, event) {
  const value = Number(event.target.value)
  if (busy.value || value === account.value[field]) {
    event.target.value = String(account.value[field])
    return
  }
  await run(() => updateAccount(account.value.id, { [field]: value }))
  event.target.value = String(account.value[field])
}
</script>

<template>
  <Modal class="member-dialog" @close="emit('close')">
    <button type="button" class="close" aria-label="關閉" @click="emit('close')">✕</button>
    <div class="head">
      <Avatar :size="84" :status="member.status" />
      <b class="title">{{ member.name }}</b>
      <span v-if="member.sub" class="sub">{{ member.sub }}</span>
    </div>

    <div class="details">
      <div class="drow"><span>帳號</span><b class="mono">{{ account.user_name }}</b></div>
      <div class="drow"><span>Session 狀態</span>
        <RouterLink v-if="member.session" :to="{ name: 'session-detail', params: { id: member.session.id } }">{{ sessionLabels[member.status] }}</RouterLink>
        <b v-else>未綁 Session</b>
      </div>
      <div class="drow"><span>帳戶狀態</span><b>{{ account.status }}</b></div>
    </div>

    <section class="part">
      <ChatPermission :account="account" @changed="emit('changed', $event)" />
    </section>

    <section class="part">
      <div class="label">禁言</div>
      <template v-if="mutedUntil">
        <p class="muted">禁言中，剩 {{ remaining }} 分鐘（到 {{ fmtTime(account.chat_muted_until) }}）</p>
        <p class="reason pre">原因：{{ account.chat_mute_reason }}</p>
        <button type="button" :disabled="busy" @click="submitUnmute">解除禁言</button>
      </template>
      <form v-else class="mute-form" @submit.prevent="submitMute">
        <label>分鐘數（1～43200）<input v-model="mute.minutes" type="number" min="1" max="43200" step="1" required :disabled="busy" /></label>
        <label>原因（最多 200 字）<input v-model="mute.reason" type="text" maxlength="200" required :disabled="busy" /></label>
        <button data-tone="neg" :disabled="busy || !muteValid">禁言</button>
      </form>
    </section>

    <section class="part">
      <div class="label">發言頻率</div>
      <div class="rate">每
        <input :value="account.chat_rate_minutes" type="number" min="1" max="1440" step="1" aria-label="頻率分鐘數" :disabled="busy" @blur="saveRate('chat_rate_minutes', $event)" @keydown.enter.prevent="$event.target.blur()" />
        分鐘最多
        <input :value="account.chat_rate_count" type="number" min="1" max="100" step="1" aria-label="頻率則數" :disabled="busy" @blur="saveRate('chat_rate_count', $event)" @keydown.enter.prevent="$event.target.blur()" />
        則
      </div>
    </section>

    <menu>
      <button type="button" data-tone="acc" @click="emit('mention', member)">@ 他</button>
    </menu>
  </Modal>
</template>

<style scoped>
.member-dialog { position: relative; max-width: 440px; max-height: calc(100vh - 32px); overflow-y: auto; border-radius: 20px; padding: 0 0 20px; border-top-width: 1px; }
.close { position: absolute; right: 8px; top: 8px; width: 44px; height: 44px; border: 0; border-radius: 12px; font-size: 16px; }
.head { display: flex; flex-direction: column; align-items: center; gap: 4px; padding: 28px 24px 16px; text-align: center; overflow-wrap: anywhere; }
.title { font-weight: 700; font-size: 20px; margin-top: 8px; }
.sub { font-size: 14px; color: var(--dim); }
.details { padding: 0 24px 8px; }
.drow { display: flex; justify-content: space-between; align-items: center; gap: 12px; min-height: 40px; font-size: 14px; border-bottom: 1px solid var(--line); }
.drow > span { color: var(--dim); flex: none; }
.drow > b, .drow > a { font-weight: 500; min-width: 0; overflow-wrap: anywhere; text-align: right; }
.part { display: flex; flex-direction: column; align-items: flex-start; gap: 8px; padding: 12px 24px 0; font-size: 14px; }
.label { font-size: 13px; font-weight: 700; color: var(--mute); }
.part :deep(select) { max-width: 100%; }
.muted { color: var(--warn); }
.reason { color: var(--dim); overflow-wrap: anywhere; }
.mute-form { display: flex; flex-direction: row; flex-wrap: wrap; align-items: flex-end; gap: 8px; width: 100%; }
.mute-form label { flex: 1 1 140px; min-width: 0; }
.mute-form input { width: 100%; }
.rate { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.rate input { width: 72px; }
menu { display: flex; justify-content: flex-end; gap: 8px; margin: 0; padding: 16px 24px 0; }
menu button { padding: 6px 13px; font-size: 12.5px; }
</style>
