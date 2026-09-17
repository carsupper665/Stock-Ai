<script setup>
import { computed, reactive, ref } from 'vue'
import Modal from '../../shared/Modal.vue'
import { createModel, updateModel } from './api.js'

const props = defineProps({ providers: Array, model: Object, providerId: String })
const emit = defineEmits(['close', 'saved'])
const form = reactive({
  model_name: props.model?.model_name ?? '',
  provider_id: props.model?.provider_id ?? props.providerId ?? '',
  model: props.model?.model ?? '',
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
    const body = { ...form, options: JSON.parse(form.options), levels: JSON.parse(form.levels) }
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
      <label>基礎 options（JSON）<textarea v-model="form.options" class="mono" rows="3" /></label>
      <label>Level 對應 options（JSON，選填）<textarea v-model="form.levels" class="mono" rows="3" /></label>
      <p class="note">Level 可用 low／medium／high／max，例如 {"high":{"reasoning_effort":"high"}}；實際 options 由 Provider 決定。</p>
      <p v-if="isCLI" class="note" data-tone="warn">CLI Harness 只收 reasoning_effort 一個 option（其餘一律退回）。Codex 可填 minimal／low／medium／high／xhigh；Claude Code 可填 low／medium／high／xhigh／max。</p>
      <label class="row"><input v-model="form.enabled" type="checkbox" />啟用</label>
      <p class="note">儲存後立即生效並保留至重啟。API Token 在 Provider 內設定，不要放入 options。</p>
      <p v-if="error" role="alert" data-tone="neg">{{ error }}</p>
      <menu><button type="button" @click="$emit('close')">取消</button><button data-tone="acc" :disabled="busy">{{ busy ? '儲存中…' : '儲存模型' }}</button></menu>
    </form>
  </Modal>
</template>
