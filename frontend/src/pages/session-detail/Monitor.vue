<script setup>
import { ref } from 'vue'
import { useLoad } from '../../core/load.js'
import { fmtClock, money, num, pnlTone } from '../../shared/format.js'
import { getLedger, getPositions, getToolStats, getUsage } from './api.js'

const props = defineProps({ session: Object })
const usage = useLoad(() => getUsage(props.session.id))
const tools = useLoad(() => getToolStats(props.session.id))
const positions = useLoad(() => getPositions(props.session.id))
const before = ref(null)
const previous = ref([])
const ledger = useLoad(() => getLedger(props.session.id, before.value))

function older() {
  const entries = ledger.data.value?.entries
  if (!entries?.length) return
  previous.value.push(before.value)
  before.value = entries.at(-1).seq
  ledger.reload()
}
function newer() {
  before.value = previous.value.pop()
  ledger.reload()
}
function freshness(load) {
  const observed = load.data.value?.observed_at
  if (observed) return `資料 ${fmtClock(observed)} · 取得 ${fmtClock(load.at.value)}`
  return load.at.value ? `上次成功 ${fmtClock(load.at.value)}` : '尚未成功更新'
}
const unknownLabels = { input_tokens: 'Input', output_tokens: 'Output', cached_tokens: 'Cached', total_tokens: 'Total' }
</script>

<template>
  <div class="monitor-columns">
    <div class="monitor-stack trading-stack">
      <section class="card col positions" :aria-busy="positions.loading.value">
        <div class="row">
          <h2 class="grow">帳戶即時部位</h2>
          <span class="mono small" data-tone="mute">{{ freshness(positions) }}</span>
          <button :disabled="positions.loading.value" @click="positions.reload">{{ positions.loading.value ? '更新中…' : '更新' }}</button>
        </div>
        <p v-if="positions.error.value" class="callout" data-tone="neg">部位載入失敗：{{ positions.error.value.message }}</p>
        <p v-if="positions.loading.value && !positions.data.value" class="note">載入部位中…</p>
        <template v-if="positions.data.value">
          <div v-if="positions.data.value.positions.length" class="table-scroll positions-scroll">
            <table>
              <thead><tr><th>Symbol</th><th>Side</th><th>Qty</th><th>Entry</th><th>Mark</th><th>SL / TP</th><th>Unrealized</th></tr></thead>
              <tbody><tr v-for="p in positions.data.value.positions" :key="p.id"><td>{{ p.symbol }}</td><td>{{ p.side }}</td><td>{{ num(p.quantity) }}</td><td>{{ num(p.entry_price) }}</td><td>{{ num(p.mark_price) }}</td><td>{{ num(p.stop_loss) }} / {{ num(p.take_profit) }}</td><td :data-tone="pnlTone(p.unrealized_pnl)">{{ money(p.unrealized_pnl) }}</td></tr></tbody>
            </table>
          </div>
          <p v-else class="empty">目前沒有未平倉部位。</p>
        </template>
      </section>

      <section class="card col ledger" :aria-busy="ledger.loading.value">
        <div class="row">
          <h2 class="grow">帳戶 Ledger · 包含非 Agent 操作</h2>
          <span class="mono small" data-tone="mute">{{ freshness(ledger) }}</span>
          <button :disabled="ledger.loading.value" @click="ledger.reload">{{ ledger.loading.value ? '更新中…' : '更新本頁' }}</button>
        </div>
        <p v-if="ledger.error.value" class="callout" data-tone="neg">Ledger 載入失敗：{{ ledger.error.value.message }}</p>
        <p v-if="ledger.loading.value && !ledger.data.value" class="note">載入 Ledger 中…</p>
        <template v-if="ledger.data.value">
          <div v-if="ledger.data.value.entries.length" class="table-scroll ledger-scroll">
            <table>
              <thead><tr><th>Seq / time</th><th>Event</th><th class="num">Balance Δ</th><th>Source</th></tr></thead>
              <tbody><tr v-for="e in ledger.data.value.entries" :key="e.seq"><td class="mono">#{{ e.seq }}<div class="small" data-tone="mute">{{ fmtClock(e.created_at) }}</div></td><td>{{ e.event }} {{ e.trigger ?? '' }}</td><td class="num" :data-tone="pnlTone(e.balance_delta)">{{ money(e.balance_delta) }}</td><td><RouterLink v-if="e.source" :to="{ name: 'session-detail', params: { id: e.source.session_id }, query: { tab: 'runs', run: e.source.run_id } }">{{ e.source.session_id === session.id ? '' : `${e.source.session_id} / ` }}Run #{{ e.source.run_id }}</RouterLink><span v-else class="note">非 Agent／未歸屬</span></td></tr></tbody>
            </table>
          </div>
          <p v-else class="empty">沒有帳戶事件。</p>
          <div class="row"><button :disabled="!previous.length || ledger.loading.value" @click="newer">較新</button><button :disabled="ledger.data.value.entries.length < 100 || ledger.loading.value" @click="older">較舊</button></div>
        </template>
      </section>
    </div>

    <div class="monitor-stack metric-stack">
      <section class="card col usage" :aria-busy="usage.loading.value">
        <div class="row">
          <h2 class="grow">模型用量</h2>
          <span class="mono small" data-tone="mute">{{ freshness(usage) }}</span>
          <button :disabled="usage.loading.value" @click="usage.reload">{{ usage.loading.value ? '更新中…' : '更新' }}</button>
        </div>
        <p v-if="usage.error.value" class="callout" data-tone="neg">用量載入失敗：{{ usage.error.value.message }}</p>
        <p v-if="usage.loading.value && !usage.data.value" class="note">載入用量中…</p>
        <template v-if="usage.data.value">
          <div v-if="usage.data.value.by_model.length" class="table-scroll usage-scroll">
            <table>
              <thead><tr><th>模型來源</th><th>Calls</th><th>Input</th><th>Output</th><th>Cached</th><th>Total</th></tr></thead>
              <tbody><tr v-for="m in usage.data.value.by_model" :key="`${m.model_name}/${m.provider_id}/${m.model}`"><td>{{ m.model_name }}<div class="mono small">{{ m.provider_id ?? '未知 Provider' }} / {{ m.model ?? '未知模型' }}</div></td><td>{{ m.model_calls }}</td><td>{{ num(m.input_tokens) }}</td><td>{{ num(m.output_tokens) }}</td><td>{{ num(m.cached_tokens) }}</td><td>{{ num(m.total_tokens) }}</td></tr></tbody>
            </table>
          </div>
          <p v-else class="empty">尚無模型呼叫用量。</p>
          <div class="stats unknown-usage">
            <div v-for="(label, key) in unknownLabels" :key="key" class="stat"><small>{{ label }} 未回報次數</small><b>{{ num(usage.data.value.unknown_attempts?.[key]) }}</b></div>
          </div>
          <p class="note">用量為已回報值合計，— 表示未知；cached 已包含於 input。</p>
        </template>
      </section>

      <section class="card col tools" :aria-busy="tools.loading.value">
        <div class="row">
          <h2 class="grow">Tool 統計</h2>
          <span class="mono small" data-tone="mute">{{ freshness(tools) }}</span>
          <button :disabled="tools.loading.value" @click="tools.reload">{{ tools.loading.value ? '更新中…' : '更新' }}</button>
        </div>
        <p v-if="tools.error.value" class="callout" data-tone="neg">Tool 統計載入失敗：{{ tools.error.value.message }}</p>
        <p v-if="tools.loading.value && !tools.data.value" class="note">載入 Tool 統計中…</p>
        <template v-if="tools.data.value">
          <div v-if="tools.data.value.tools.length" class="table-scroll tools-scroll">
            <table>
              <thead><tr><th>工具</th><th>Calls</th><th>成功 / 錯誤 / 執行中</th><th class="num">平均 ms</th><th class="num">最大 ms</th><th class="num">最後 ms</th></tr></thead>
              <tbody><tr v-for="t in tools.data.value.tools" :key="t.tool_name"><td>{{ t.tool_name }}</td><td>{{ t.call_count }}</td><td>{{ t.success_count }} / {{ t.error_count }} / {{ t.in_flight_count }}</td><td class="num">{{ num(t.avg_latency_ms) }}</td><td class="num">{{ num(t.max_latency_ms) }}</td><td class="num">{{ num(t.last_latency_ms) }}</td></tr></tbody>
            </table>
          </div>
          <p v-else class="empty">尚無可顯示的 Tool 統計。</p>
        </template>
      </section>
    </div>
  </div>
</template>

<style scoped>
.monitor-columns { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 8px; align-items: start; width: 100%; }
.monitor-stack { display: flex; flex-direction: column; gap: 8px; min-width: 0; }
.card { min-width: 0; padding: 9px 10px; gap: 7px; }
.table-scroll { overflow: auto; }
.positions-scroll { max-height: 300px; }
.ledger-scroll { max-height: 430px; }
.usage-scroll { max-height: 320px; }
.tools-scroll { max-height: 430px; }
.unknown-usage { grid-template-columns: repeat(4, minmax(90px, 1fr)); }
@media (max-width: 1050px) { .monitor-columns { grid-template-columns: 1fr; } }
</style>
