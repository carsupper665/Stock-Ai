<script setup>
import { computed } from 'vue'
import { fmtTime, money, num, pnlTone, tone } from './format.js'

// 訂單、成交、Ledger 共用的兩行式列表；只列出該筆真的有的欄位。
const props = defineProps({ kind: String, items: Array, sessionId: String })
const sides = { buy: '買', sell: '賣' }
const orderTypes = { market: '市價', limit: '限價' }
const statuses = { open: '掛單中', filled: '已成交', canceled: '已撤單', rejected: '已拒絕' }
const events = {
  order_placed: '下單', fill: '成交', order_rejected: '下單被拒', order_canceled: '撤單',
  stops_updated: '更新停損停利', max_dd_triggered: '風控停止', max_dd_resumed: '解除風控',
}
const triggers = { stop_loss: '停損觸發', take_profit: '停利觸發', max_dd: '風控強平' }
const fill = (quantity, price) => (price ? `${num(quantity)} @ ${num(price)}` : num(quantity))

function order(o) {
  return {
    key: o.id, title: o.symbol,
    tags: [
      { text: sides[o.side] ?? o.side, tone: tone(o.side) }, { text: orderTypes[o.type] ?? o.type },
      o.leverage > 1 ? { text: `${o.leverage}x` } : null, o.reduce_only ? { text: '只減倉' } : null, o.product ? { text: o.product } : null,
    ],
    status: { text: statuses[o.status] ?? o.status, tone: tone(o.status) },
    amount: o.realized_pnl ? { label: '已實現', value: money(o.realized_pnl), tone: pnlTone(o.realized_pnl) } : null,
    facts: [
      ['數量', num(o.quantity)], o.price ? ['價格', num(o.price)] : null,
      o.filled_quantity ? ['成交量', fill(o.filled_quantity, o.avg_fill_price)] : null, o.fee ? ['手續費', num(o.fee, 4)] : null,
      o.stop_loss ? ['停損', num(o.stop_loss)] : null, o.take_profit ? ['停利', num(o.take_profit)] : null,
    ],
    error: o.reject_reason, trigger: o.trigger, refs: [o.id, o.position_id], source: o.source, time: o.created_at,
  }
}

function trade(t) {
  return {
    key: t.id, title: t.symbol,
    tags: [{ text: sides[t.side] ?? t.side, tone: tone(t.side) }, t.role ? { text: t.role } : null, t.product ? { text: t.product } : null],
    amount: t.realized_pnl ? { label: '已實現', value: money(t.realized_pnl), tone: pnlTone(t.realized_pnl) } : null,
    facts: [['成交', fill(t.quantity, t.price)], t.fee ? ['手續費', num(t.fee, 4)] : null],
    refs: [t.id, t.order_id], source: t.source, time: t.created_at,
  }
}

function entry(e) {
  return {
    key: e.seq, title: events[e.event] ?? e.event, seq: e.seq, tags: [],
    amount: e.balance_delta ? { label: '餘額', value: money(e.balance_delta), tone: pnlTone(e.balance_delta) } : null,
    facts: [
      e.quantity ? ['數量', fill(e.quantity, e.price)] : null, e.fee ? ['手續費', num(e.fee, 4)] : null,
      e.realized_pnl ? ['已實現', money(e.realized_pnl)] : null, e.balance_delta ? ['變動後餘額', num(e.balance_after, 2)] : null,
      e.stop_loss ? ['停損', num(e.stop_loss)] : null, e.take_profit ? ['停利', num(e.take_profit)] : null,
    ],
    trigger: e.trigger, refs: [e.order_id || e.position_id], source: e.source, time: e.created_at,
  }
}

const builders = { orders: order, trades: trade, ledger: entry }
const rows = computed(() => (props.items ?? []).map((item) => {
  const row = builders[props.kind](item)
  return { ...row, tags: row.tags.filter(Boolean), facts: row.facts.filter(Boolean), refs: row.refs.filter(Boolean) }
}))
const runLink = (source) => (source?.session_id ? { name: 'session-detail', params: { id: source.session_id }, query: { tab: 'runs', run: source.run_id } } : null)
</script>

<template>
  <ol class="activity">
    <li v-for="row in rows" :key="row.key" class="item">
      <div class="row line">
        <span v-if="row.seq" class="mono small" data-tone="mute">#{{ row.seq }}</span>
        <b class="title">{{ row.title }}</b>
        <span v-for="tag in row.tags" :key="tag.text" class="tag outline" :data-tone="tag.tone">{{ tag.text }}</span>
        <span v-if="row.trigger" class="tag" data-tone="warn">{{ triggers[row.trigger] ?? row.trigger }}</span>
        <span class="grow"></span>
        <span v-if="row.amount" class="amount" :data-tone="row.amount.tone"><small>{{ row.amount.label }}</small> {{ row.amount.value }}</span>
        <span v-if="row.status" class="tag" :data-tone="row.status.tone">{{ row.status.text }}</span>
      </div>
      <div v-if="row.facts.length" class="facts">
        <span v-for="[label, value] in row.facts" :key="label"><small>{{ label }}</small> {{ value }}</span>
      </div>
      <p v-if="row.error" class="note" data-tone="neg">{{ row.error }}</p>
      <div class="meta small" data-tone="mute">
        <span v-for="ref in row.refs" :key="ref" class="mono">{{ ref }}</span>
        <RouterLink v-if="runLink(row.source)" :to="runLink(row.source)">{{ sessionId && row.source.session_id !== sessionId ? `${row.source.session_id} / ` : '' }}Run #{{ row.source.run_id }}</RouterLink>
        <span>{{ fmtTime(row.time) }}</span>
      </div>
    </li>
  </ol>
</template>

<style scoped>
.activity { list-style: none; margin: 0; padding: 0; }
.item { display: flex; flex-direction: column; gap: 4px; padding: 10px 12px; border-bottom: 1px solid var(--line); }
.line { gap: 6px; flex-wrap: wrap; }
.title { font-size: 14px; }
.amount { font-size: 14px; font-weight: 600; font-variant-numeric: tabular-nums; }
.amount small, .facts small { color: var(--mute); font-weight: 400; font-size: 11.5px; }
.facts { display: flex; flex-wrap: wrap; gap: 4px 16px; font-size: 13px; font-variant-numeric: tabular-nums; }
.meta { display: flex; flex-wrap: wrap; gap: 4px 12px; }
</style>
