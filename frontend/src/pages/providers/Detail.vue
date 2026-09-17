<script setup>
import { computed, reactive, ref } from 'vue'

import { confirm } from '../../shared/confirm.js'
import { fmtTime } from '../../shared/format.js'
import Modal from '../../shared/Modal.vue'
import { say } from '../../shared/toast.js'
import { createToken, deleteProvider, deleteToken, updateProvider, updateToken, testProvider } from './api.js'

const props = defineProps({ provider: Object, harnesses: Array })
const isCLI = computed(() => ['codex', 'claude_code'].includes(props.provider.type))
const testing = ref(false)
const testError = ref('')
const testResult = ref(null)
const testModel = ref(props.provider.default_model ?? '')
const testPrompt = ref('Reply with a short confirmation that this provider invocation works.')
async function invokeTest() {
  if (testing.value) return
  testing.value = true; testError.value = ''; testResult.value = null
  try { testResult.value = await testProvider(props.provider.id, { model: testModel.value || null, prompt: testPrompt.value }) }
  catch (e) { testError.value = `${e.code ?? e.status}: ${e.message}` }
  finally { testing.value = false }
}
const emit = defineEmits(['changed', 'deleted'])

async function run(fn, done) {
  try {
    await fn()
    say(done)
    emit('changed')
  } catch (e) {
    say(e.message, 'neg')
  }
}

async function toggle() {
  const off = props.provider.enabled
  const ok = await confirm({
    title: `${off ? '停用' : '啟用'} Provider「${props.provider.id}」？`,
    tone: off ? 'warn' : 'acc',
    ok: off ? '確認停用' : '確認啟用',
    lines: off ? ['用它的 Session 下次叫模型會失敗（PROVIDER_DISABLED）', '模型目錄裡對到它的 model 會變成不可用'] : ['恢復可用；模型目錄下次查詢即反映'],
  })
  if (ok) run(() => updateProvider(props.provider.id, { enabled: !off }), off ? '已停用' : '已啟用')
}
async function remove() {
  const n = props.provider.tokens?.length ?? 0
  const ok = await confirm({ title: `刪除 Provider「${props.provider.id}」？`, ok: '確認刪除', lines: ['用它的 Session 下次叫模型會失敗（PROVIDER_NOT_FOUND）', `底下 ${n} 個 API Token 一併刪除、立即失效`, '不可復原'] })
  if (!ok) return
  try {
    await deleteProvider(props.provider.id)
    say('已刪除')
    emit('deleted')
  } catch (e) {
    say(e.message, 'neg')
  }
}

const editing = ref(false)
const edit = reactive({ name: '', base_url: '', default_model: '', auth_mode: 'api_key' })
const editError = ref('')
function openEdit() {
  Object.assign(edit, { name: props.provider.name, base_url: props.provider.base_url, default_model: props.provider.default_model ?? '', auth_mode: props.provider.config?.auth_mode ?? 'api_key' })
  editError.value = ''
  editing.value = true
}
async function saveEdit() {
  try {
    await updateProvider(props.provider.id, { name: edit.name, base_url: isCLI.value ? '' : edit.base_url, default_model: edit.default_model || null, ...(isCLI.value ? { config: { auth_mode: edit.auth_mode } } : {}) })
    editing.value = false
    say('已更新')
    emit('changed')
  } catch (e) {
    editError.value = e.message
  }
}

// 明文 token 只在送出那一次經過瀏覽器：不保存、不快取、送出即清空（L-12）
const adding = ref(false)
const form = reactive({ name: '', token: '' })
const addError = ref('')
async function addToken() {
  addError.value = ''
  try {
    const t = await createToken(props.provider.id, { name: form.name, token: form.token })
    form.token = ''
    form.name = ''
    adding.value = false
    say(`已新增 token「${t.name}」（${t.masked}），明文不會再顯示`)
    emit('changed')
  } catch (e) {
    form.token = ''
    addError.value = e.message
  }
}
async function rotate(t) {
  const secret = await confirm({ title: `換 token「${t.name}」的 secret？`, tone: 'warn', ok: '確認替換', lines: ['舊 secret 立即失效', '明文只輸入這一次，送出後只看得到 masked', '前端不保存、不快取'], input: { label: '新的明文 token', placeholder: '貼上明文 token（送出後永遠看不到）', mono: true, type: 'password' } })
  if (secret) run(() => updateToken(props.provider.id, t.id, { token: secret }), '已替換 secret')
}
async function toggleToken(t) {
  const ok = await confirm({ title: `${t.enabled ? '停用' : '啟用'} token「${t.name}」？`, tone: t.enabled ? 'warn' : 'acc', ok: '確認', lines: [t.enabled ? '停用後這把不再被選用；若沒有其他啟用的 token 會出現 TOKEN_UNAVAILABLE' : '恢復可被選用'] })
  if (ok) run(() => updateToken(props.provider.id, t.id, { enabled: !t.enabled }), t.enabled ? '已停用' : '已啟用')
}
async function removeToken(t) {
  const ok = await confirm({ title: `刪除 token「${t.name}」？`, ok: '確認刪除', lines: ['立即失效、不可復原', '若沒有其他啟用的 token，用這家的 Session 會出現 TOKEN_UNAVAILABLE'] })
  if (ok) run(() => deleteToken(props.provider.id, t.id), '已刪除 token')
}
</script>

<template>
  <div class="col side">
    <section class="card col">
      <div class="row">
        <span class="mono id">{{ provider.id }}</span>
        <span class="tag" data-tone="acc">{{ provider.type }}</span>
        <span class="grow"></span>
        <button @click="openEdit">編輯</button>
        <button @click="toggle">{{ provider.enabled ? '停用' : '啟用' }}</button>
        <button data-tone="neg" @click="remove">刪除</button>
      </div>
      <dl class="kv narrow">
        <dt>name</dt><dd>{{ provider.name }}</dd>
        <dt>base_url</dt><dd class="mono">{{ provider.base_url }}</dd>
        <dt>default_model</dt><dd class="mono">{{ provider.default_model ?? '—' }}</dd>
        <dt>config</dt><dd class="mono">{{ JSON.stringify(provider.config) }}{{ isCLI ? '（認證方式；CLI 路徑由 Server 管理）' : '（憑證走 API Token）' }}</dd>
        <dt>enabled</dt><dd class="mono" :data-tone="provider.enabled ? 'pos' : 'mute'">{{ provider.enabled }}</dd>
        <dt>updated_at</dt><dd class="mono">{{ fmtTime(provider.updated_at) }}</dd>
      </dl>
      <p class="note">id 與 type 建立後不可改；其餘欄位用「編輯」（PATCH）。</p>
    </section>

    <section class="card col">
      <h2>測試呼叫</h2>
      <label>模型 ID<input v-model="testModel" placeholder="留空使用 default_model" /></label>
      <label>測試提示<textarea v-model="testPrompt" rows="2" maxlength="8192" /></label>
      <button :disabled="testing || !provider.enabled" data-tone="acc" @click="invokeTest">{{ testing ? 'CLI／Provider 執行中…' : '發送一次測試請求' }}</button>
      <p class="note">實際呼叫模型，可能產生 API 費用或消耗訂閱額度；不會建立 Session 或執行交易工具。</p>
      <p v-if="testError" role="alert" data-tone="neg">{{ testError }}</p>
      <pre v-if="testResult">{{ JSON.stringify(testResult, null, 2) }}</pre>
    </section>

    <section class="card col">
      <div class="row">
        <span class="eyebrow grow">API Tokens</span>
        <button data-tone="acc" @click="adding = true">+ 新增 token</button>
      </div>
      <div v-if="provider.tokens" class="rows">
        <div v-for="t in provider.tokens" :key="t.id" class="row">
          <span class="name">{{ t.name }}</span>
          <span class="mono grow" data-tone="dim">{{ t.masked }}</span>
          <span class="mono small" :data-tone="t.enabled ? 'pos' : 'mute'">{{ t.enabled ? 'enabled' : 'disabled' }}</span>
          <button @click="rotate(t)">換 secret</button>
          <button @click="toggleToken(t)">{{ t.enabled ? '停用' : '啟用' }}</button>
          <button class="text" data-tone="neg" @click="removeToken(t)">刪除</button>
        </div>
        <p v-if="!provider.tokens.length" class="note">{{ isCLI && provider.config?.auth_mode === 'local_login' ? '使用 Server 本機 CLI 登入，不需要另外新增 API Token。' : '還沒有 token；需要憑證的 Provider 請先新增。' }}</p>
      </div>
      <p v-else class="note" data-tone="neg">token 列表讀不到</p>
      <p class="note">API Token 依建立時間、ID 穩定選取第一把啟用者；Server 不自動輪替或重試。</p>
    </section>

    <Modal v-if="editing" @close="editing = false">
      <form @submit.prevent="saveEdit">
        <h2>編輯 Provider「{{ provider.id }}」</h2>
        <label>name <input v-model="edit.name" required /></label>
        <label v-if="!isCLI">base_url <input v-model="edit.base_url" class="mono" type="url" required /></label>
        <label v-else>認證方式<select v-model="edit.auth_mode"><option value="api_key">Provider API Token</option><option value="local_login">Server 本機 CLI 登入</option></select></label>
        <label>default_model（留空＝清除）<input v-model="edit.default_model" class="mono" /></label>
        <p v-if="editError" data-tone="neg">{{ editError }}</p>
        <menu>
          <button type="button" @click="editing = false">取消</button>
          <button data-tone="acc">儲存</button>
        </menu>
      </form>
    </Modal>

    <Modal v-if="adding" @close="adding = false; form.token = ''">
      <form @submit.prevent="addToken">
        <h2>新增 API Token</h2>
        <label>name <input v-model="form.name" required /></label>
        <label>明文 token（送出後永遠看不到，只回 masked）<input v-model="form.token" class="mono" type="password" autocomplete="off" required /></label>
        <p class="note">不能含空白或控制字元；前端不保存、不快取，送出即清空。</p>
        <p v-if="addError" data-tone="neg">{{ addError }}</p>
        <menu>
          <button type="button" @click="adding = false; form.token = ''">取消</button>
          <button data-tone="acc">送出</button>
        </menu>
      </form>
    </Modal>
  </div>
</template>

<style scoped>
.side {
  flex: 1 1 440px;
}
.card {
  gap: 10px;
}
.id {
  font-size: 14px;
  font-weight: 500;
}
.name {
  width: 96px;
  flex: none;
  font-size: 12.5px;
  font-weight: 500;
}
</style>
