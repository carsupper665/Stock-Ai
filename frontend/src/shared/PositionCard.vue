<script setup>
import { computed } from 'vue'
import { fmtTime, money, num, pnlTone } from './format.js'

// 只顯示後端真的有的欄位；沒有的就不出現，不推算。
const props = defineProps({ position: Object })
const p = computed(() => props.position)
const roi = computed(() => (p.value.margin > 0 && p.value.unrealized_pnl != null ? (p.value.unrealized_pnl / p.value.margin) * 100 : null))
const cells = computed(() => [
  ['持倉量', p.value.quantity],
  ['保證金', p.value.margin > 0 ? p.value.margin : null],
  ['持倉均價', p.value.entry_price],
  ['標記價格', p.value.mark_price],
].filter(([, value]) => value != null))
const stops = computed(() => [
  ['停損', p.value.stop_loss, p.value.stop_loss_source],
  ['停利', p.value.take_profit, p.value.take_profit_source],
].filter(([, value]) => value))
const runLink = (source) => (source?.session_id ? { name: 'session-detail', params: { id: source.session_id }, query: { tab: 'runs', run: source.run_id } } : null)
</script>

<template>
  <article class="card col position">
    <div class="row">
      <b class="symbol">{{ p.symbol }}</b>
      <span class="tag" :data-tone="p.side === 'short' ? 'neg' : 'pos'">{{ p.side === 'short' ? '空' : p.side === 'long' ? '多' : p.side }}</span>
      <span v-if="p.leverage" class="tag outline">{{ p.leverage }}x</span>
      <span v-if="p.product" class="tag outline">{{ p.product }}</span>
    </div>
    <div class="row headline">
      <div class="col grow"><small>未實現盈虧</small><b class="pnl" :data-tone="pnlTone(p.unrealized_pnl)">{{ money(p.unrealized_pnl) }}</b></div>
      <div v-if="roi != null" class="col end"><small>回報率</small><b class="pnl" :data-tone="pnlTone(roi)">{{ roi > 0 ? '+' : '' }}{{ roi.toFixed(2) }}%</b></div>
    </div>
    <dl class="cells">
      <div v-for="[label, value] in cells" :key="label"><dt>{{ label }}</dt><dd>{{ label === '保證金' ? num(value, 2) : num(value) }}</dd></div>
      <div v-for="[label, value, source] in stops" :key="label">
        <dt>{{ label }}</dt>
        <dd>{{ num(value) }}<template v-if="source"> <RouterLink v-if="runLink(source)" :to="runLink(source)" class="small">Run #{{ source.run_id }}</RouterLink></template></dd>
      </div>
    </dl>
    <div v-if="p.opened_at" class="foot small" data-tone="mute">開倉 {{ fmtTime(p.opened_at) }}</div>
  </article>
</template>

<style scoped>
.position { gap: 10px; }
.symbol { font-size: 16px; }
.headline small, .cells dt { color: var(--mute); font-size: 11.5px; }
.pnl { font-size: 20px; font-variant-numeric: tabular-nums; }
.end { align-items: flex-end; }
.cells { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px 12px; margin: 0; }
.cells dd { margin: 2px 0 0; font-size: 14px; font-variant-numeric: tabular-nums; }
.foot { border-top: 1px solid var(--line); padding-top: 8px; }
</style>
