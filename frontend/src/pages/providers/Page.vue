<script setup>
import { computed, reactive, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import Modal from '../../shared/Modal.vue'
import { say } from '../../shared/toast.js'
import { createProvider, listModels, listProviders, listTokens, harnessStatus } from './api.js'
import Detail from './Detail.vue'
import ModelEditor from './ModelEditor.vue'

const route = useRoute()
const router = useRouter()

// 列表要順便知道每家有幾個啟用的 token（L-13：TOKEN_UNAVAILABLE 的前置警示）
const { data, error, loading, reload } = useLoad(async () => {
  const providers = await listProviders()
  const tokens = await Promise.all(providers.map((p) => listTokens(p.id).catch(() => null)))
  return providers.map((p, i) => ({ ...p, tokens: tokens[i] }))
})
const models = useLoad(() => listModels().then((r) => r.models))
const harnesses = useLoad(harnessStatus)
const editingModel = ref(undefined)
async function modelSaved() {
  editingModel.value = undefined
  await models.reload()
  say('模型已儲存，可在建立 Session 時選用')
}
async function providerChanged() { await Promise.all([reload(), models.reload()]) }

const selectedId = computed(() => route.query.id ?? data.value?.[0]?.id)
const selected = computed(() => data.value?.find((p) => p.id === selectedId.value))
const select = (p) => router.replace({ query: { ...route.query, id: p.id } })

const types = ['openai', 'anthropic', 'gemini', 'openai_compatible', 'codex', 'claude_code']
const creating = ref(false)
const form = reactive({ id: '', name: '', type: 'openai_compatible', base_url: '', default_model: '', enabled: true })
const authMode = ref('api_key')
const isCLI = computed(() => ['codex', 'claude_code'].includes(form.type))
const cliStatus = computed(() => harnesses.data.value?.harnesses?.find((h) => h.type === form.type))
watch(() => form.type, (type, previous) => {
  if (!['codex', 'claude_code'].includes(type)) {
    if (['codex', 'claude_code'].includes(previous)) form.default_model = ''
    return
  }
  form.base_url = ''
  form.default_model = type === 'codex' ? 'cli-default' : 'claude-sonnet-4-6'
  authMode.value = harnesses.data.value?.harnesses?.find((h) => h.type === type)?.local_login_configured ? 'local_login' : 'api_key'
})
const createError = ref('')
async function create() {
  createError.value = ''
  try {
    const p = await createProvider({ ...form, base_url: isCLI.value ? '' : form.base_url, default_model: form.default_model || null, config: isCLI.value ? { auth_mode: authMode.value } : {} })
    creating.value = false
    say(`已建立 Provider「${p.id}」，可先按「測試呼叫」，再新增模型供 Session 選用`)
    await reload()
    select(p)
  } catch (e) {
    createError.value = e.message
  }
}
</script>

<template>
  <article>
    <p v-if="error" class="callout" data-tone="neg">{{ error.message }}</p>
    <p v-else-if="loading && !data" class="note">載入中…</p>
    <div v-else-if="data" class="cols">
      <div class="col list">
        <div class="row">
          <span class="eyebrow grow">Providers</span>
          <button data-tone="acc" @click="creating = true">+ 新增</button>
        </div>
        <section v-for="p in data" :key="p.id" class="card col prov" :data-mark="p.id === selectedId ? 'acc' : ''" data-click @click="select(p)">
          <div class="row">
            <span class="mono id">{{ p.id }}</span>
            <span class="grow"></span>
            <span class="mono small" :data-tone="p.enabled ? 'pos' : 'mute'">{{ p.enabled ? 'enabled' : 'disabled' }}</span>
          </div>
          <span data-tone="dim">{{ p.name }}</span>
          <div class="row">
            <span class="tag" data-tone="acc">{{ p.type }}</span>
            <span class="mono small" data-tone="mute">LLM API</span>
            <span class="grow"></span>
            <span class="mono small" :data-tone="p.tokens?.filter((t) => t.enabled).length ? 'mute' : 'neg'">{{ p.tokens ? p.tokens.filter((t) => t.enabled).length : '?' }} token 啟用</span>
          </div>
        </section>
        <p v-if="!data.length" class="empty">先新增 Provider 與 API Token，再於下方新增模型，即可建立 Session 使用。</p>
        <p class="hint" data-tone="mute">服務健康不代表模型認證成功。選擇 Provider 後可用「測試呼叫」實際驗證；這會發送一次模型請求。</p>
        <section class="card col"><span class="eyebrow">Server CLI 狀態</span><p v-if="harnesses.error.value" data-tone="neg">{{ harnesses.error.value.message }}</p><div v-for="h in harnesses.data.value?.harnesses ?? []" :key="h.type" class="row"><span>{{ h.type }} · {{ h.version }}</span><span :data-tone="h.executable_configured ? 'pos' : 'neg'">{{ h.executable_configured ? '已安裝' : '未設定執行檔' }}</span><span class="note">{{ h.local_login_configured ? '已設定本機登入檔（效期以測試結果為準）' : '需 API Token 或先在 Server 登入' }}</span></div></section>

        <section class="card col">
          <div class="row"><span class="eyebrow grow">模型目錄</span><button data-tone="acc" :disabled="!data.length" @click="editingModel = null">+ 新增模型</button></div>
          <p v-if="models.error.value" class="note" data-tone="warn">拿不到：{{ models.error.value.message }}（要在代理設定 LLM_SERVER_RUNTIME_TOKEN）</p>
          <div v-else-if="models.data.value" class="rows">
            <div v-for="m in models.data.value" :key="m.model_name" class="row">
              <span class="mono">{{ m.model_name }}</span>
              <span class="mono small" data-tone="mute">→ {{ m.provider_id }} / {{ m.model }}</span>
              <span class="grow"></span>
              <span class="mono small" data-tone="mute">{{ Object.keys(m.levels ?? {}).join('/') || '無 level' }}</span>
              <span class="mono small" :data-tone="m.available ? 'pos' : 'neg'">{{ m.available ? 'available' : m.enabled ? 'provider 不可用' : 'disabled' }}</span>
              <button @click="editingModel = m">編輯</button>
            </div>
            <p v-if="!models.data.value.length" class="note">尚無模型，按「新增模型」選擇 Provider 並填入上游模型 ID，無須重啟。</p>
          </div>
        </section>
      </div>

      <Detail v-if="selected" :key="selected.id" :provider="selected" :harnesses="harnesses.data.value?.harnesses ?? []" @changed="providerChanged" @deleted="router.replace({ query: {} }).then(providerChanged)" />
    </div>

    <ModelEditor v-if="editingModel !== undefined" :providers="data ?? []" :model="editingModel" :provider-id="selectedId" @close="editingModel = undefined" @saved="modelSaved" />
    <Modal v-if="creating" @close="creating = false">
      <form @submit.prevent="create">
        <h2>新增 Provider</h2>
        <label>id（自己取，之後模型目錄靠它對應；例 deepseek-main）<input v-model="form.id" class="mono" pattern="[A-Za-z0-9][A-Za-z0-9_\-]{0,63}" required /></label>
        <label>name <input v-model="form.name" required /></label>
        <label>type <select v-model="form.type"><option v-for="t in types" :key="t" :value="t">{{ t }}</option></select></label>
        <label v-if="!isCLI">base_url（http／https，例 https://api.openai.com/v1）<input v-model="form.base_url" class="mono" type="url" required /></label>
        <template v-else>
          <label>認證方式<select v-model="authMode"><option value="api_key">Provider API Token</option><option value="local_login">Server 本機 CLI 登入</option></select></label>
          <p class="note" :data-tone="cliStatus?.executable_configured ? 'dim' : 'neg'">{{ cliStatus?.executable_configured ? 'Gateway 會啟動已設定的 CLI，使用獨立工作目錄，只有 Web 工具；Domain Tool 交回 Agent 執行。' : 'Gateway 尚未設定 CLI 執行檔；請先完成 dist/.env 的 Harness 設定。' }}</p>
          <p v-if="authMode === 'local_login' && !cliStatus?.local_login_configured" data-tone="warn">尚未設定本機登入檔，建立後需先在 Server 登入才能測試。</p>
        </template>
        <label>default_model（選填）<input v-model="form.default_model" class="mono" /></label>
        <label class="row"><input v-model="form.enabled" type="checkbox" /> enabled</label>
        <p class="note">CLI 不填 base_url。Codex 模型填 cli-default 時採用 CLI 預設模型，亦可明確指定帳號支援的模型 ID。</p>
        <p v-if="createError" data-tone="neg">{{ createError }}</p>
        <menu>
          <button type="button" @click="creating = false">取消</button>
          <button data-tone="acc">建立</button>
        </menu>
      </form>
    </Modal>
  </article>
</template>

<style scoped>
.list {
  flex: 1 1 300px;
  gap: 9px;
}
.prov {
  gap: 3px;
}
.id {
  font-size: 12.5px;
  font-weight: 500;
}
</style>
