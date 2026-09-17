<script setup>
import { computed, onMounted, reactive, ref } from 'vue'

import Modal from '../../shared/Modal.vue'
import { say } from '../../shared/toast.js'
import { createSession, listAccounts, listBindings, listModels } from './api.js'

const props = defineProps({ preset: Object })
const emit = defineEmits(['close', 'created'])

const form = reactive({
  name: props.preset ? `${props.preset.name}（複製）` : '',
  account_id: '',
  model_name: props.preset?.model_name ?? '',
  model_level: props.preset?.model_level ?? '',
  prompt: props.preset?.prompt ?? '',
  max_loop: props.preset?.max_loop ?? '',
  max_tool_call: props.preset?.max_tool_call ?? '',
})
const accounts = ref(null)
const bound = ref(new Map())
const models = ref(null)
const modelsError = ref('')
const error = ref('')
const busy = ref(false)

onMounted(async () => {
  // 帳號一個 Session 只能綁一次（ACCOUNT_ALREADY_BOUND），刪掉 Session 也不釋放，所以要連已刪除的一起算；
  // 模型目錄要 LLM Server 的 runtime 憑證，拿不到就手填
  const [a, s, m] = await Promise.allSettled([listAccounts(), listBindings(), listModels()])
  accounts.value = a.status === 'fulfilled' ? a.value.accounts : []
  bound.value = new Map(s.status === 'fulfilled' ? s.value.sessions.map((x) => [x.account_id, x]) : [])
  if (m.status === 'fulfilled') models.value = m.value.models
  else modelsError.value = m.reason.message
})

const levels = computed(() => Object.keys(models.value?.find((m) => m.model_name === form.model_name)?.levels ?? {}))

async function submit() {
  busy.value = true
  error.value = ''
  const body = { name: form.name, account_id: form.account_id, model_name: form.model_name }
  if (form.model_level) body.model_level = form.model_level
  if (form.prompt.trim()) body.prompt = form.prompt
  if (form.max_loop !== '') body.max_loop = Number(form.max_loop)
  if (form.max_tool_call !== '') body.max_tool_call = Number(form.max_tool_call)
  try {
    const created = await createSession(body)
    say(`已建立「${created.name}」（stopped，不自動啟動）`)
    emit('created', created)
  } catch (e) {
    error.value = e.message
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal @close="$emit('close')">
    <form @submit.prevent="submit">
      <h2>建立 Session</h2>
      <p class="note">建好是 stopped，不會自動啟動；建立後設定不可改，要改就建新的（A-10、A-11）。</p>
      <label>name <input v-model="form.name" required /></label>
      <label>
        account_id（交易帳號，一個帳號只能綁一個 Session）
        <select v-model="form.account_id" required>
          <option value="" disabled>{{ accounts ? (accounts.length ? '選擇帳號' : '沒有帳號，先到「交易帳號」建立') : '載入中…' }}</option>
          <option v-for="a in accounts" :key="a.id" :value="a.id" :disabled="a.status !== 'active' || bound.has(a.id)">
            {{ a.user_name }} · {{ a.id }}{{ a.status !== 'active' ? '（disabled）' : bound.has(a.id) ? `（已被「${bound.get(a.id).name}」綁定${bound.get(a.id).deleted_at ? '，該 Session 已刪除但綁定不釋放' : ''}）` : '' }}
          </option>
        </select>
      </label>
      <p v-if="accounts && accounts.length && accounts.every((a) => a.status !== 'active' || bound.has(a.id))" class="note" data-tone="warn">
        現有帳號都已被綁定（含已刪除的 Session）。綁定在審計紀錄的生命週期內不釋放，請到「交易帳號」建立新帳號。
      </p>
      <label>
        model_name
        <select v-if="models?.length" v-model="form.model_name" required>
          <option value="" disabled>選擇模型</option>
          <option v-for="m in models" :key="m.model_name" :value="m.model_name" :disabled="!m.available">{{ m.model_name }} → {{ m.provider_id }} / {{ m.model }}{{ m.available ? '' : '（不可用）' }}</option>
        </select>
        <input v-else v-model="form.model_name" class="mono" placeholder="手填；Server 會對 /v1/models 驗證" required />
        <span v-if="modelsError" class="note" data-tone="warn">模型目錄拿不到（{{ modelsError }}），改為手填</span>
      </label>
      <label>
        model_level（選填）
        <select v-if="levels.length" v-model="form.model_level"><option value="">不指定</option><option v-for="l in levels" :key="l" :value="l">{{ l }}</option></select>
        <input v-else v-model="form.model_level" class="mono" placeholder="留空＝不指定；例 low / high" />
      </label>
      <label>prompt（留空＝Server Default，前端查不到內容 · 待定 4）<textarea v-model="form.prompt" rows="3"></textarea></label>
      <div class="row">
        <label class="grow">max_loop（留空＝Server Default）<input v-model="form.max_loop" type="number" min="1" /></label>
        <label class="grow">max_tool_call（留空＝Server Default）<input v-model="form.max_tool_call" type="number" min="1" /></label>
      </div>
      <p class="note">超過 max_loop／max_tool_call 的 Run 直接 failed（A-09）。</p>
      <p v-if="error" data-tone="neg">{{ error }}</p>
      <menu>
        <button type="button" @click="$emit('close')">取消</button>
        <button data-tone="acc" :disabled="busy">建立</button>
      </menu>
    </form>
  </Modal>
</template>
