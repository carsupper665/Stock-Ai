<script setup>
import { computed, reactive, ref } from 'vue'
import Modal from '../../shared/Modal.vue'
import { createModel, updateModel } from './api.js'

const props = defineProps({ providers: Array, model: Object, providerId: String })
const emit = defineEmits(['close', 'saved'])
const windowUnits = { K: 1000, M: 1000000 }
function formatWindow(tokens) {
  if (!tokens) return '128K'
  if (tokens % windowUnits.M === 0) return `${tokens / windowUnits.M}M`
  return tokens % windowUnits.K === 0 ? `${tokens / windowUnits.K}K` : String(tokens)
}
function parseWindow(text) {
  const match = /^([1-9]\d*)\s*([KM])$/i.exec(text.trim())
  if (!match) throw new Error('上下文視窗格式為數字＋K 或 M，例如 48K、256K、1M')
  return Number(match[1]) * windowUnits[match[2].toUpperCase()]
}
const form = reactive({
  model_name: props.model?.model_name ?? '',
  provider_id: props.model?.provider_id ?? props.providerId ?? '',
  model: props.model?.model ?? '',
  context_window: formatWindow(props.model?.context_window),
  enabled: props.model?.enabled ?? true,
  options: JSON.stringify(props.model?.options ?? {}, null, 2),
  levels: JSON.stringify(props.model?.levels ?? {}, null, 2),
})
const isCLI = computed(() => ['codex', 'claude_code'].includes(props.providers.find((p) => p.id === form.provider_id)?.type))
const error = ref('')
const busy = ref(false)
async function save() {
  if (busy.value) return
  error.value = ''
  busy.value = true
  try {
    const body = { ...form, context_window: parseWindow(form.context_window), options: JSON.parse(form.options), levels: JSON.parse(form.levels) }
    for (const field of ['options', 'levels']) {
      if (!body[field] || Array.isArray(body[field]) || typeof body[field] !== 'object') throw new Error(`${field} 必須是 JSON 物件`)
    }
    const saved = props.model ? await updateModel(props.model.model_name, body) : await createModel(body)
    emit('saved', saved)
  } catch (e) { error.value = e.message } finally { busy.value = false }
}
</script>

<template>
  <Modal @close="$emit('close')">
    <form @submit.prevent="save">
      <h2>{{ model ? '編輯模型' : '新增模型' }}</h2>
      <label>模型名稱（Session 選用的名稱）<input v-model="form.model_name" :readonly="!!model" pattern="[A-Za-z0-9][A-Za-z0-9_.\-]{0,127}" required /></label>
      <label>Provider<select v-model="form.provider_id" required><option value="" disabled>選擇 Provider</option><option v-for="p in providers" :key="p.id" :value="p.id">{{ p.name }} · {{ p.id }}{{ p.enabled ? '' : '（停用）' }}</option></select></label>
      <label>上游模型 ID<input v-model="form.model" placeholder="Provider 實際提供的模型 ID" required /></label>
      <label>上下文視窗（數字＋K 或 M）<input v-model="form.context_window" class="mono" placeholder="例如 48K、256K、1M" required /></label>
      <label>基礎 options（JSON）<textarea v-model="form.options" class="mono" rows="3" /></label>
      <label>Level 對應 options（JSON，選填）<textarea v-model="form.levels" class="mono" rows="3" /></label>
      <p class="note">Level 名稱自訂（英數字、_ . -），例如 {"xhigh":{"reasoning_effort":"xhigh"}}；值是否有效由 Provider 決定，可先用 Provider 頁的測試呼叫確認。</p>
      <p v-if="isCLI" class="note" data-tone="warn">CLI Harness 只收 reasoning_effort 一個 option（其餘一律退回）。Claude Code 遇到不認得的值會改用預設、不會報錯。</p>
      <label class="row"><input v-model="form.enabled" type="checkbox" />啟用</label>
      <p class="note">儲存後立即生效並保留至重啟。API Token 在 Provider 內設定，不要放入 options。</p>
      <p v-if="error" role="alert" data-tone="neg">{{ error }}</p>
      <menu><button type="button" @click="$emit('close')">取消</button><button data-tone="acc" :disabled="busy">{{ busy ? '儲存中…' : '儲存模型' }}</button></menu>
    </form>
  </Modal>
</template>
