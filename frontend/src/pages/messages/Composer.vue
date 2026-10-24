<script setup>
import { computed, nextTick, ref } from 'vue'

import Avatar from '../../shared/Avatar.vue'
import { say } from '../../shared/toast.js'
import { postMessage } from './api.js'
import { add } from './store.js'

const LIMIT = 500
const props = defineProps({ members: Array, disabled: Boolean })
const emit = defineEmits(['sent'])

const input = ref(null)
const draft = ref('')
const tags = ref([])
const busy = ref(false)
const count = computed(() => Array.from(draft.value).length)
const canSend = computed(() => !busy.value && !props.disabled && draft.value.trim() !== '' && count.value <= LIMIT)

const memberOf = (tag) => props.members.find((member) => member.account.user_name === tag)
function addTag(userName) {
  if (!tags.value.includes(userName)) tags.value = [...tags.value, userName]
}
const removeTag = (userName) => { tags.value = tags.value.filter((tag) => tag !== userName) }

// @ 選單：游標前面是「@字串」時打開，用字串篩選成員的名稱或帳號名稱。
const mention = ref(null)
const active = ref(0)
const matches = computed(() => {
  if (!mention.value) return []
  const query = mention.value.query.toLowerCase()
  return props.members.filter((member) => !tags.value.includes(member.account.user_name) &&
    (member.name.toLowerCase().includes(query) || member.account.user_name.toLowerCase().includes(query)))
})
function updateMention() {
  const caret = input.value.selectionStart
  const found = draft.value.slice(0, caret).match(/@([^\s@]*)$/)
  mention.value = found ? { start: caret - found[0].length, end: caret, query: found[1] } : null
  active.value = 0
}
async function pick(member) {
  const { start, end } = mention.value
  draft.value = draft.value.slice(0, start) + draft.value.slice(end)
  addTag(member.account.user_name)
  mention.value = null
  await nextTick()
  input.value.focus()
  input.value.setSelectionRange(start, start)
}

function keydown(event) {
  if (mention.value) {
    const total = matches.value.length
    if (event.key === 'ArrowDown' && total) { event.preventDefault(); active.value = (active.value + 1) % total; return }
    if (event.key === 'ArrowUp' && total) { event.preventDefault(); active.value = (active.value - 1 + total) % total; return }
    if (event.key === 'Enter' && total) { event.preventDefault(); pick(matches.value[active.value]); return }
    if (event.key === 'Escape') { event.preventDefault(); mention.value = null; return }
  }
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    send()
  }
}

async function send() {
  if (!canSend.value) return
  busy.value = true
  try {
    add([await postMessage(draft.value, tags.value)])
    draft.value = ''
    tags.value = []
    emit('sent')
  } catch (e) {
    say(e.message, 'neg')
  } finally {
    busy.value = false
  }
}

defineExpose({ addTag, focus: () => input.value.focus() })
</script>

<template>
  <form class="composer" @submit.prevent="send">
    <div v-if="tags.length" class="chips">
      <span v-for="tag in tags" :key="tag" class="chip">
        <span class="ellipsis">@{{ memberOf(tag)?.name ?? tag }}</span>
        <button type="button" :aria-label="`移除 @${memberOf(tag)?.name ?? tag}`" @click="removeTag(tag)">✕</button>
      </span>
    </div>
    <div class="line">
      <div class="field">
        <ul v-if="mention" class="mentions" role="listbox" aria-label="提及成員">
          <li v-for="(member, index) in matches" :key="member.id" role="option" :aria-selected="index === active" @mousedown.prevent="pick(member)" @mousemove="active = index">
            <Avatar :size="28" :status="member.status" />
            <span class="names"><b class="ellipsis">{{ member.name }}</b><span v-if="member.sub" class="sub ellipsis">{{ member.sub }}</span></span>
          </li>
          <li v-if="!matches.length" class="none">沒有符合的成員</li>
        </ul>
        <textarea ref="input" v-model="draft" rows="1" placeholder="輸入訊息，打 @ 提及成員" aria-label="輸入訊息" :disabled="disabled" @input="updateMention" @click="updateMention" @keydown="keydown" @blur="mention = null"></textarea>
      </div>
      <span class="mono count" :data-tone="count > LIMIT ? 'neg' : 'mute'">{{ count }}/{{ LIMIT }}</span>
      <button type="submit" class="send" aria-label="傳送" title="傳送" :disabled="!canSend">
        <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true"><path fill="currentColor" d="M3 20.5v-6l8-2.5-8-2.5v-6l19 8.5z" /></svg>
      </button>
    </div>
  </form>
</template>

<style scoped>
.composer { display: flex; flex-direction: column; gap: 8px; padding: 10px 12px; background: var(--surf); border-top: 1px solid var(--line); }
.chips { display: flex; flex-wrap: wrap; gap: 6px; }
.chip { display: inline-flex; align-items: center; gap: 4px; max-width: 100%; height: 30px; padding: 0 4px 0 12px; border-radius: 15px; border: 1px solid var(--acc); color: var(--acc); background: var(--accw); font-size: 13px; }
.chip button { border: 0; width: 24px; height: 24px; padding: 0; border-radius: 12px; color: inherit; flex: none; }
.line { display: flex; align-items: flex-end; gap: 8px; }
.field { position: relative; flex: 1; min-width: 0; }
textarea { display: block; width: 100%; min-height: 44px; max-height: 160px; resize: none; border-radius: 22px; padding: 11px 18px; background: var(--surf2); border-color: var(--line); font-size: 15px; line-height: 1.4; field-sizing: content; }
.count { flex: none; align-self: center; }
.send { width: 44px; height: 44px; border-radius: 50%; border: 0; padding: 0; background: var(--acc); color: var(--surf); display: flex; align-items: center; justify-content: center; flex: none; }
.send:hover:not(:disabled) { background: var(--acc); color: var(--surf); filter: brightness(0.92); }
.mentions { position: absolute; left: 0; bottom: calc(100% + 6px); z-index: 4; width: min(320px, 100%); max-height: 260px; overflow-y: auto; margin: 0; padding: 6px; list-style: none; background: var(--surf); border: 1px solid var(--line); border-radius: 14px; box-shadow: var(--modal-shadow); }
.mentions li { display: flex; align-items: center; gap: 10px; min-height: 44px; padding: 0 10px; border-radius: 10px; cursor: pointer; }
.mentions li[aria-selected='true'] { background: var(--surf2); }
.mentions .none { color: var(--mute); cursor: default; }
.names { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.names b { font-size: 14px; }
.sub { font-size: 12px; color: var(--dim); }
</style>
