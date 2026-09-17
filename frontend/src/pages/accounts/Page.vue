<script setup>
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'

import { useLoad } from '../../core/load.js'
import { fmtTime, num, tone } from '../../shared/format.js'
import Modal from '../../shared/Modal.vue'
import { say } from '../../shared/toast.js'
import { createAccount, listAccounts, listSessions } from './api.js'
import Detail from './Detail.vue'

const route = useRoute()
const router = useRouter()

const { data, error, loading, reload } = useLoad(async () => {
  const [{ accounts }, sessions] = await Promise.all([listAccounts(), listSessions().then((r) => r.sessions).catch(() => null)])
  return { accounts, sessions }
})
const sessionOf = (accountId) => data.value.sessions?.find((s) => s.account_id === accountId)

const selectedId = computed(() => route.query.id ?? data.value?.accounts[0]?.id)
const selected = computed(() => data.value?.accounts.find((a) => a.id === selectedId.value))
const select = (a) => router.replace({ query: { ...route.query, id: a.id } })

const creating = ref(false)
const form = reactive({ user_name: '', initial_balance: 10000 })
const createError = ref('')
async function create() {
  createError.value = ''
  try {
    const a = await createAccount({ user_name: form.user_name, initial_balance: Number(form.initial_balance) })
    creating.value = false
    say(`已建立「${a.user_name}」，Account Token 在右側，只有 USER 看得到`)
    await reload()
    select(a)
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
      <div class="card flush list">
        <div class="row">
          <span class="eyebrow grow">虛擬帳號</span>
          <button data-tone="acc" @click="creating = true">+ 建立帳號</button>
        </div>
        <table>
          <thead>
            <tr><th>帳號</th><th>Status</th><th class="num">Initial</th><th class="num">Balance</th><th>被哪個 Session 用</th><th>Created</th></tr>
          </thead>
          <tbody>
            <tr v-for="a in data.accounts" :key="a.id" data-click :data-active="a.id === selectedId ? '' : null" @click="select(a)">
              <td>
                <div class="name">{{ a.user_name }}</div>
                <div class="mono small" data-tone="mute">{{ a.id }}</div>
              </td>
              <td><span class="tag" :data-tone="tone(a.status)">{{ a.status }}</span></td>
              <td class="num" data-tone="dim">{{ num(a.initial_balance, 2) }}</td>
              <td class="num">{{ num(a.balance, 2) }}</td>
              <td>
                <template v-if="sessionOf(a.id)">
                  <RouterLink :to="{ name: 'session-detail', params: { id: sessionOf(a.id).id } }" @click.stop>{{ sessionOf(a.id).name }}</RouterLink>
                  <span v-if="sessionOf(a.id).deleted_at" class="mono small" data-tone="warn" title="綁定在審計紀錄的生命週期內不釋放">（已刪除，仍佔用）</span>
                </template>
                <span v-else class="mono" data-tone="mute">{{ data.sessions ? '未綁定' : 'Agent Server 讀不到' }}</span>
              </td>
              <td class="mono" data-tone="mute">{{ fmtTime(a.created_at) }}</td>
            </tr>
            <tr v-if="!data.accounts.length"><td colspan="6" class="note">還沒有帳號。建一個，拿到 Account Token 後 Agent 才能交易。</td></tr>
          </tbody>
        </table>
        <div class="row foot"><span class="tag pending">待定 2</span><span class="note">「被哪個 Session 用」由前端拿 Agent Server 的 Session 列表反查 account_id；交易後端無此欄位。</span></div>
      </div>

      <Detail v-if="selected" :key="selected.id" :account="selected" :session="sessionOf(selected.id)" @changed="reload" @deleted="router.replace({ query: {} }).then(reload)" />
    </div>

    <Modal v-if="creating" @close="creating = false">
      <form @submit.prevent="create">
        <h2>建立虛擬帳號</h2>
        <label>user_name（≤ 64 字，不可重複）<input v-model="form.user_name" required /></label>
        <label>initial_balance（> 0）<input v-model="form.initial_balance" type="number" min="0.00000001" step="any" required /></label>
        <p class="note">建立後會拿到 Account Token；名字重複回 name_taken。</p>
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
  flex: 1 1 420px;
}
.list table {
  min-width: 560px;
}
.name {
  font-size: 12.5px;
  font-weight: 500;
}
.foot {
  padding: 8px 12px;
}
</style>
