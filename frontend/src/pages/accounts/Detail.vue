<script setup>
import { computed, ref, watch } from 'vue'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { fmtTime, money, num, pnlTone, tone } from '../../shared/format.js'
import ActivityList from '../../shared/ActivityList.vue'
import PositionCard from '../../shared/PositionCard.vue'
import { copy, say } from '../../shared/toast.js'
import { deleteAccount, listLedger, listOrders, listPositions, listTrades, resetToken, resumeTrading, selfAccount, updateAccount } from './api.js'

const props = defineProps({ account: Object, session: Object, refresh: Function })
const emit = defineEmits(['changed', 'deleted'])

const masked = computed(() => `${props.account.token.slice(0, 6)}…${props.account.token.slice(-4)}`)
// 四個數字只有 Account Token 自查拿得到；帳號 token 的 401 只影響這一塊，不會登出 USER
const summary = useLoad(() => selfAccount(props.account.token))

const tab = ref('positions')
const product = ref('')
const status = ref('')
const limit = ref(50)
const rows = useLoad(load)
function load() {
  const id = props.account.id
  if (tab.value === 'positions') return listPositions(id, product.value).then((r) => r.positions)
  if (tab.value === 'orders') return listOrders(id, status.value, limit.value).then((r) => r.orders)
  if (tab.value === 'ledger') return listLedger(id, limit.value).then((r) => r.entries)
  return listTrades(id, limit.value).then((r) => r.trades)
}
watch([tab, product, status, limit], () => { rows.data.value = null; rows.reload() }, { flush: 'sync' })

async function run(fn, done) {
  try {
    await fn()
    say(done)
    emit('changed')
  } catch (e) {
    say(e.message, 'neg')
  }
}
async function rename() {
  const name = await confirm({ title: '改名', tone: 'acc', ok: '確認改名', lines: ['留言板上已發過的留言會跟著顯示新名字'], input: { label: 'user_name', value: props.account.user_name } })
  if (name) run(() => updateAccount(props.account.id, { user_name: name }), '已改名')
}
async function toggleStatus() {
  const disable = props.account.status === 'active'
  const ok = await confirm({
    title: `${disable ? '停用' : '啟用'}帳號「${props.account.user_name}」？`,
    tone: disable ? 'warn' : 'acc',
    ok: disable ? '確認停用' : '確認啟用',
    lines: disable ? ['不能交易（account_disabled），既有部位仍可查看', '用這個 token 的 Agent 會收到 401'] : ['恢復可交易'],
  })
  if (ok) run(() => updateAccount(props.account.id, { status: disable ? 'disabled' : 'active' }), disable ? '已停用' : '已啟用')
}
async function reset() {
  const ok = await confirm({ title: '重設 Account Token？', ok: '確認重設', lines: ['舊 token 立即失效', '正在用它的 Agent 會立刻斷線（Run 可能變 failed）', '新 token 顯示在這裡，只有 USER 看得到'] })
  if (ok) run(() => resetToken(props.account.id), '已重設 token')
}
async function remove() {
  // 刪除會連帶清掉部位與未成交掛單，數量先問後端；問不到就只說會一併刪除，不要因此擋住刪除
  const [p, o] = await Promise.allSettled([listPositions(props.account.id, ''), listOrders(props.account.id, 'open', 200)])
  const counts = p.status === 'fulfilled' && o.status === 'fulfilled'
    ? `未平部位 ${p.value.positions.length} 筆與未成交掛單 ${o.value.orders.length} 筆會一併刪除（成交紀錄與 Ledger 保留）`
    : '未平部位與未成交掛單會一併刪除（成交紀錄與 Ledger 保留）'
  const ok = await confirm({ title: `刪除帳號「${props.account.user_name}」？`, ok: '確認刪除', lines: ['不可復原', counts, '它在留言板發過的留言作者變 [deleted]', '使用中的 Account Token 立即失效', props.session ? `Session「${props.session.name}」綁著這個帳號，之後會找不到帳號` : '沒有 Session 綁定它'] })
  if (!ok) return
  try {
    await deleteAccount(props.account.id)
    say('已刪除')
    emit('deleted')
  } catch (e) {
    say(e.message, 'neg')
  }
}
// resuming 擋住 pending 中的重送；失敗不清停止標記，等 server 回應或重讀帳號後再更新。
const resuming = ref(false)
async function resume() {
  const ok = await confirm({
    title: `解除「${props.account.user_name}」的風控停止？`,
    tone: 'acc',
    ok: '確認解除',
    lines: ['沿用 max_dd 和原始額度，從本次全平後權益重新起算', '新 baseline = 全平後權益，loss_limit 仍以原始額度計算', '需已無部位與未成交掛單，否則會被拒絕'],
  })
  if (!ok || resuming.value) return
  resuming.value = true
  try {
    await resumeTrading(props.account.id)
    say('已解除風控停止')
    emit('changed')
  } catch (e) {
    say(e.message, 'neg')
    // 連線中斷或 5xx 無法判斷 POST 是否已提交；以 server 重讀結果為準，不自動重送。
    if (!e.status || e.status >= 500) await props.refresh()
  } finally {
    resuming.value = false
  }
}
</script>

<template>
  <div class="col side">
    <section class="card col">
      <div class="row">
        <h2>{{ account.user_name }}</h2>
        <span class="tag" :data-tone="tone(account.status)">{{ account.status }}</span>
        <span class="grow"></span>
        <button @click="rename">改名</button>
        <button @click="toggleStatus">{{ account.status === 'active' ? '停用' : '啟用' }}</button>
        <button data-tone="neg" @click="remove">刪除</button>
      </div>
      <div class="inset row token">
        <div class="mono small wide" data-tone="mute">ACCOUNT TOKEN</div>
        <span class="mono grow">{{ masked }}</span>
        <button @click="copy(account.token, '已複製 Account Token')">複製</button>
        <button data-tone="neg" @click="reset">重設</button>
      </div>
      <div v-if="account.max_dd != null" class="inset col risk">
        <div class="row"><span class="eyebrow">最大虧損 max_dd</span><span class="grow"></span><span class="tag" data-tone="warn">{{ account.max_dd }}%</span></div>
        <div class="row mono small" data-tone="dim">
          <span>baseline {{ num(account.max_dd_baseline_equity, 2) }}</span>
          <span>loss_limit {{ num(account.max_dd_loss_limit, 2) }}</span>
          <span>floor {{ num(account.max_dd_equity_floor, 2) }}</span>
        </div>
        <p v-if="account.max_dd_stopped_at" data-tone="neg">風控停止：{{ fmtTime(account.max_dd_stopped_at) }}；Account Token 與讀取不受影響</p>
        <button v-if="account.max_dd_stopped_at" class="wide" data-tone="acc" :disabled="resuming" @click="resume">{{ resuming ? '解除中…' : '解除風控停止' }}</button>
      </div>
      <p v-else class="note">最大虧損 max_dd：未設定</p>
      <p v-if="summary.error.value" class="empty">讀取失敗：{{ summary.error.value.message }}</p>
      <div v-else-if="summary.data.value" class="stats">
        <div v-for="k in ['equity', 'available', 'locked_margin', 'unrealized_pnl']" :key="k" class="stat big">
          <small>{{ k }}</small>
          <b :data-tone="k === 'unrealized_pnl' ? pnlTone(summary.data.value[k]) : k === 'locked_margin' ? 'dim' : null">{{ k === 'unrealized_pnl' ? money(summary.data.value[k]) : `$${num(summary.data.value[k], 2)}` }}</b>
        </div>
      </div>
      <p v-else class="note">載入中…</p>
    </section>

    <section class="card flush col">
      <div class="row head">
        <span class="pills">
          <button v-for="t in ['positions', 'orders', 'trades', 'ledger']" :key="t" :aria-selected="tab === t" @click="tab = t">{{ { positions: '部位', orders: '訂單', trades: '成交', ledger: 'Ledger' }[t] }}</button>
        </span>
        <span class="grow"></span>
        <select v-if="tab === 'positions'" v-model="product"><option value="">全部 product</option><option value="spot">spot</option><option value="futures">futures</option></select>
        <select v-if="tab === 'orders'" v-model="status"><option value="">全部 status</option><option v-for="s in ['open', 'filled', 'canceled', 'rejected']" :key="s" :value="s">{{ s }}</option></select>
        <select v-if="tab !== 'positions'" v-model="limit"><option v-for="n in [20, 50, 100, 200]" :key="n" :value="n">limit {{ n }}</option></select>
      </div>
      <p v-if="rows.error.value" class="callout pad" data-tone="neg">{{ rows.error.value.message }}</p>
      <div v-else-if="rows.data.value" class="scroll">
        <div v-if="tab === 'positions'" class="position-cards">
          <PositionCard v-for="p in rows.data.value" :key="p.id" :position="p" />
          <p v-if="!rows.data.value.length" class="empty">目前沒有未平倉部位。</p>
        </div>
        <template v-else>
          <ActivityList :kind="tab" :items="rows.data.value" />
          <p v-if="!rows.data.value.length" class="note pad">沒有資料</p>
        </template>
      </div>
      <p v-else class="note pad">載入中…</p>
    </section>
  </div>
</template>

<style scoped>
.position-cards {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  gap: 10px;
  padding: 10px;
}
.side {
  flex: 1 1 380px;
}
.card {
  gap: 10px;
}
.token {
  gap: 8px;
}
.risk {
  gap: 6px;
}
.risk p {
  margin: 0;
  font-size: 12px;
}
.wide {
  width: 100%;
}
.head {
  padding: 9px 11px 0;
}
table {
  min-width: 640px;
}
.pad {
  margin: 0 13px 13px;
}
</style>
