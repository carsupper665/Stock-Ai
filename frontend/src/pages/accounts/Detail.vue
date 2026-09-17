<script setup>
import { computed, ref, watch } from 'vue'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { fmtTime, money, num, pnlTone, tone } from '../../shared/format.js'
import { copy, say } from '../../shared/toast.js'
import { deleteAccount, listLedger, listOrders, listPositions, listTrades, resetToken, selfAccount, updateAccount } from './api.js'

const props = defineProps({ account: Object, session: Object })
const emit = defineEmits(['changed', 'deleted'])

const masked = computed(() => `${props.account.token.slice(0, 6)}…${props.account.token.slice(-4)}`)
// 待定 6：四個數字只有 Account Token 自查拿得到；帳號 token 的 401 只影響這一塊，不會登出 USER
const summary = useLoad(() => selfAccount(props.account.token))

const tab = ref('positions')
const product = ref('')
const status = ref('')
const limit = ref(50)
const hints = { positions: 'product 可篩 spot / futures', orders: 'limit 預設 50、最多 200、最新在前（無游標）', trades: 'limit 預設 50、最多 200、最新在前（無游標）', ledger: 'limit 預設 50、最多 200、seq 由新到舊；只有 fill 會動餘額' }
const rows = useLoad(load)
function load() {
  const id = props.account.id
  if (tab.value === 'positions') return listPositions(id, product.value).then((r) => r.positions)
  if (tab.value === 'orders') return listOrders(id, status.value, limit.value).then((r) => r.orders)
  if (tab.value === 'ledger') return listLedger(id, limit.value).then((r) => r.entries)
  return listTrades(id, limit.value).then((r) => r.trades)
}
// 追溯（X-07）：任何 run_id 都能點到該 Session 的 Run；沒綁 Session 時只顯示文字
const runLink = (source) => (source?.session_id ? { name: 'session-detail', params: { id: source.session_id }, query: { tab: 'runs', run: source.run_id } } : null)
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
      <p v-if="summary.error.value" class="empty">代查失敗：{{ summary.error.value.message }}</p>
      <div v-else-if="summary.data.value" class="stats">
        <div v-for="k in ['equity', 'available', 'locked_margin', 'unrealized_pnl']" :key="k" class="stat big">
          <small>{{ k }}</small>
          <b :data-tone="k === 'unrealized_pnl' ? pnlTone(summary.data.value[k]) : k === 'locked_margin' ? 'dim' : null">{{ k === 'unrealized_pnl' ? money(summary.data.value[k]) : `$${num(summary.data.value[k], 2)}` }}</b>
        </div>
      </div>
      <p v-else class="note">載入中…</p>
      <div class="row sep"><span class="tag pending">待定 6</span><span class="note">這四個數字目前只有 Account Token 自查得到（GET /v1/account），畫面以該帳號的 token 代查。</span></div>
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
        <span class="mono small" data-tone="mute">{{ hints[tab] }}</span>
      </div>
      <p v-if="rows.error.value" class="callout pad" data-tone="neg">{{ rows.error.value.message }}</p>
      <div v-else-if="rows.data.value" class="scroll">
        <table v-if="tab === 'positions'">
          <thead><tr><th>Symbol</th><th>Product</th><th>Side</th><th class="num">Qty</th><th class="num">Entry</th><th class="num">Mark</th><th class="num">Lev</th><th class="num">Margin</th><th class="num">SL / TP</th><th class="num">Unrealized</th><th>Opened</th></tr></thead>
          <tbody>
            <tr v-for="p in rows.data.value" :key="p.id">
              <td class="mono">{{ p.symbol }}</td><td class="mono small" data-tone="dim">{{ p.product }}</td>
              <td><span class="tag" :data-tone="tone(p.side)">{{ p.side }}</span></td>
              <td class="num">{{ num(p.quantity) }}</td><td class="num">{{ num(p.entry_price) }}</td><td class="num">{{ num(p.mark_price) }}</td>
              <td class="num" data-tone="dim">{{ p.leverage }}x</td><td class="num" data-tone="dim">{{ num(p.margin, 2) }}</td>
              <td class="num" data-tone="dim">
                {{ p.stop_loss ? num(p.stop_loss) : '—' }}<template v-if="p.stop_loss_source"> <RouterLink v-if="runLink(p.stop_loss_source)" :to="runLink(p.stop_loss_source)">#{{ p.stop_loss_source.run_id }}</RouterLink><span v-else>#{{ p.stop_loss_source.run_id }}</span></template>
                / {{ p.take_profit ? num(p.take_profit) : '—' }}<template v-if="p.take_profit_source"> <RouterLink v-if="runLink(p.take_profit_source)" :to="runLink(p.take_profit_source)">#{{ p.take_profit_source.run_id }}</RouterLink><span v-else>#{{ p.take_profit_source.run_id }}</span></template>
              </td>
              <td class="num" :data-tone="pnlTone(p.unrealized_pnl)">{{ money(p.unrealized_pnl) }}</td>
              <td class="mono" data-tone="mute">{{ fmtTime(p.opened_at) }}</td>
            </tr>
          </tbody>
        </table>
        <table v-else-if="tab === 'orders'">
          <thead><tr><th>Order</th><th>Symbol</th><th>Side / Type</th><th class="num">Qty</th><th class="num">Price</th><th class="num">Filled</th><th class="num">Avg</th><th class="num">Lev</th><th class="num">SL / TP</th><th class="num">Fee</th><th class="num">Realized</th><th>Status</th><th>Run</th><th>Created</th></tr></thead>
          <tbody>
            <tr v-for="o in rows.data.value" :key="o.id">
              <td class="mono small" data-tone="mute">{{ o.id }}</td>
              <td class="mono">{{ o.symbol }} <span class="small" data-tone="dim">{{ o.product }}</span></td>
              <td class="mono" :data-tone="tone(o.side)">{{ o.side }} · {{ o.type }}{{ o.reduce_only ? ' · reduce' : '' }}</td>
              <td class="num">{{ num(o.quantity) }}</td><td class="num">{{ o.price ? num(o.price) : 'mkt' }}</td>
              <td class="num">{{ num(o.filled_quantity) }}</td><td class="num">{{ o.avg_fill_price ? num(o.avg_fill_price) : '—' }}</td>
              <td class="num" data-tone="dim">{{ o.leverage }}x</td>
              <td class="num" data-tone="dim">{{ o.stop_loss ? num(o.stop_loss) : '—' }} / {{ o.take_profit ? num(o.take_profit) : '—' }}</td>
              <td class="num" data-tone="dim">{{ num(o.fee, 4) }}</td>
              <td class="num" :data-tone="pnlTone(o.realized_pnl)">{{ money(o.realized_pnl) }}</td>
              <td><span class="tag" :data-tone="tone(o.status)">{{ o.status }}</span><div v-if="o.reject_reason" class="note" data-tone="neg">{{ o.reject_reason }}</div></td>
              <td class="mono">
                <RouterLink v-if="runLink(o.source)" :to="runLink(o.source)">#{{ o.source.run_id }}</RouterLink>
                <span v-else :data-tone="o.source ? null : 'mute'">{{ o.source ? `#${o.source.run_id}` : '—' }}</span>
                <span v-if="o.trigger" class="small" data-tone="warn"> {{ o.trigger }}</span>
              </td>
              <td class="mono" data-tone="mute">{{ fmtTime(o.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        <table v-else-if="tab === 'ledger'">
          <thead><tr><th class="num">Seq</th><th>Event</th><th>Ref</th><th class="num">Qty</th><th class="num">Price</th><th class="num">Fee</th><th class="num">Realized</th><th class="num">Δ Balance</th><th class="num">Balance After</th><th>Run</th><th>Time</th></tr></thead>
          <tbody>
            <tr v-for="e in rows.data.value" :key="e.seq">
              <td class="num" data-tone="mute">{{ e.seq }}</td>
              <td class="mono">{{ e.event }}<span v-if="e.trigger" class="small" data-tone="warn"> {{ e.trigger }}</span></td>
              <td class="mono small" data-tone="mute">{{ e.order_id || e.position_id || '—' }}</td>
              <td class="num">{{ num(e.quantity) }}</td><td class="num">{{ e.price ? num(e.price) : '—' }}</td>
              <td class="num" data-tone="dim">{{ e.fee ? num(e.fee, 4) : '—' }}</td>
              <td class="num" :data-tone="e.event === 'fill' ? pnlTone(e.realized_pnl) : 'mute'">{{ e.event === 'fill' ? money(e.realized_pnl) : '—' }}</td>
              <td class="num" :data-tone="e.event === 'fill' ? pnlTone(e.balance_delta) : 'mute'">{{ e.event === 'fill' ? money(e.balance_delta) : '—' }}</td>
              <td class="num" data-tone="dim">{{ e.event === 'fill' ? num(e.balance_after, 2) : '—' }}</td>
              <td class="mono"><RouterLink v-if="runLink(e.source)" :to="runLink(e.source)">#{{ e.source.run_id }}</RouterLink><span v-else :data-tone="e.source ? null : 'mute'">{{ e.source ? `#${e.source.run_id}` : '—' }}</span></td>
              <td class="mono" data-tone="mute">{{ fmtTime(e.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        <table v-else>
          <thead><tr><th>Trade</th><th>Order</th><th>Symbol</th><th>Side</th><th>Role</th><th class="num">Qty</th><th class="num">Price</th><th class="num">Fee</th><th class="num">Realized</th><th>Run</th><th>Time</th></tr></thead>
          <tbody>
            <tr v-for="t in rows.data.value" :key="t.id">
              <td class="mono small" data-tone="mute">{{ t.id }}</td><td class="mono small" data-tone="mute">{{ t.order_id }}</td>
              <td class="mono">{{ t.symbol }} <span class="small" data-tone="dim">{{ t.product }}</span></td>
              <td class="mono" :data-tone="tone(t.side)">{{ t.side }}</td><td class="mono small" data-tone="dim">{{ t.role }}</td>
              <td class="num">{{ num(t.quantity) }}</td><td class="num">{{ num(t.price) }}</td><td class="num" data-tone="dim">{{ num(t.fee, 4) }}</td>
              <td class="num" :data-tone="pnlTone(t.realized_pnl)">{{ money(t.realized_pnl) }}</td>
              <td class="mono"><RouterLink v-if="runLink(t.source)" :to="runLink(t.source)">#{{ t.source.run_id }}</RouterLink><span v-else :data-tone="t.source ? null : 'mute'">{{ t.source ? `#${t.source.run_id}` : '—' }}</span></td>
              <td class="mono" data-tone="mute">{{ fmtTime(t.created_at) }}</td>
            </tr>
          </tbody>
        </table>
        <p v-if="!rows.data.value.length" class="note pad">沒有資料</p>
      </div>
      <p v-else class="note pad">載入中…</p>
      <p v-if="tab === 'orders'" class="note pad" data-tone="neg">rejected 訂單的 reject_reason 直接顯示在 STATUS 欄下方，不用點開。</p>
    </section>
  </div>
</template>

<style scoped>
.side {
  flex: 1 1 380px;
}
.card {
  gap: 10px;
}
.token {
  gap: 8px;
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
