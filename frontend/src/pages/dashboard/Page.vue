<script setup>
import { computed, onUnmounted, ref } from 'vue'

import { useLoad } from '../../core/load.js'
import { confirm } from '../../shared/confirm.js'
import { money, num, pnlTone } from '../../shared/format.js'
import { say } from '../../shared/toast.js'
import { listBindings, stopSession } from '../sessions/api.js'
import AgentRow from './AgentRow.vue'
import { alerts, bar, counts, filterAgents, models, overview, ranks, sortAgents, toAgent } from './agents.js'

// 連已刪除的一起拿，「已刪除」篩選才有東西；每 5 秒靜默更新
const { data, error, loading, reload } = useLoad(() => listBindings().then((r) => r.sessions.map(toAgent)))
const timer = setInterval(() => { if (!loading.value) reload() }, 5000)
onUnmounted(() => clearInterval(timer))

const filter = ref('running')
const sort = ref('pnl')
const open = ref(null)

const agents = computed(() => data.value ?? [])
const tally = computed(() => counts(agents.value))
const kpis = computed(() => overview(agents.value))
const alert = computed(() => alerts(agents.value))
const rankOf = computed(() => ranks(agents.value))
const pnls = computed(() => agents.value.filter((a) => a.pnl != null).map((a) => a.pnl))
const rows = computed(() => sortAgents(filterAgents(agents.value, filter.value), sort.value))
const modelRows = computed(() => models(agents.value))
const modelPcts = computed(() => modelRows.value.filter((m) => m.avgPct != null).map((m) => m.avgPct))

const known = computed(() => agents.value.filter((a) => a.status !== 'deleted').length)
const executing = computed(() => agents.value.filter((a) => a.executing).length)
const pill = computed(() => {
  const { running, stopped } = tally.value
  return `${running} sessions running${stopped ? ` · ${stopped} stopped` : ''} · ${executing.value ? `${executing.value} 個 Run 執行中` : '等待下一個 Event'}`
})

const filters = [['running', 'running'], ['failed', '異常'], ['holding', '持倉中'], ['flat', '空手']]
const lifeFilters = [['stopped', '已停止'], ['deleted', '已刪除']]
const sorts = [['pnl', 'PnL 排名'], ['urgent', '異常優先'], ['runs', 'Run 數'], ['tokens', 'Token 用量']]
const empty = { stopped: '目前沒有已停止的 agent。', deleted: '目前沒有已刪除的 agent。刪除的 agent 只會在這裡出現，資料仍保留。' }
function pick(key) { filter.value = key; open.value = null }

const pct = (v) => `${v > 0 ? '+' : v < 0 ? '−' : ''}${num(Math.abs(v), 2)}%`

async function stop(a) {
  const ok = await confirm({
    title: `stop Session「${a.name}」？`,
    tone: 'warn',
    ok: '確認 stop',
    lines: [
      `這是 hard stop：${a.executing ? `當前 Run #${a.last.run_id} 會變成 interrupted` : '沒有進行中的 Run，只是把狀態改回 stopped'}`,
      '所有 active Event 立即清空，不會自動重建',
      '進行中的模型／Tool 請求會被取消',
      '下次 start 會以 session_restart 開一個新 Run',
    ],
  })
  if (!ok) return
  try {
    const r = await stopSession(a.id)
    say(r.cancellation_pending ? `已送出 stop「${a.name}」，Run 取消中` : `已 stop「${a.name}」`)
  } catch (e) {
    say(e.message, 'neg')
  }
  reload()
}
</script>

<template>
  <article class="dash">
    <div class="top">
      <div class="titles">
        <div class="title"><h1>Agent 交易監控</h1><span class="paper mono">虛擬盤</span></div>
        <p class="lede">{{ known }} 個 agent，依績效、倉位與健康度整理</p>
      </div>
      <span v-if="data" class="pill mono"><i :class="{ live: executing }"></i>{{ pill }}</span>
    </div>

    <p v-if="error && !data" class="callout" data-tone="neg">{{ error.message }}</p>
    <p v-else-if="!data" class="note">載入中…</p>
    <div v-else-if="!agents.length" class="empty">還沒有 agent。到「Session 總覽」建立 Session 並 start 之後，這裡就會出現。</div>
    <template v-else>
      <p v-if="error" class="callout" data-tone="neg">更新失敗，畫面停在上次成功的資料：{{ error.message }}</p>

      <div class="summary">
        <section aria-label="總覽" class="kpis">
          <div v-for="k in kpis" :key="k.label" class="kpi">
            <span class="label">{{ k.label }}</span>
            <span class="value mono" :data-tone="k.tone">{{ k.value }}</span>
            <span class="label">{{ k.sub }}</span>
          </div>
        </section>

        <section v-if="alert.items.length" aria-label="需要處理" class="alerts">
          <div class="alert-head">
            <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"></path><path d="M12 9v4M12 17h.01"></path></svg>
            <div>
              <h2>{{ alert.items.length }} 個 Agent 最近一次 Run 失敗</h2>
              <p v-if="alert.cause" class="label">{{ alert.cause }}</p>
            </div>
          </div>
          <RouterLink v-for="i in alert.items" :key="i.id" :to="{ name: 'session-detail', params: { id: i.id }, query: { tab: 'runs', run: i.run } }" class="alert">
            <b class="mono">{{ i.name }}</b>
            <span class="risk" :data-tone="i.holding ? 'warn' : 'dim'">{{ i.risk }}</span>
            <span class="mono why">#{{ i.run }} · {{ i.message }}</span>
          </RouterLink>
        </section>
      </div>

      <section aria-label="Agent 列表" class="agents">
        <div class="controls">
          <div role="group" aria-label="篩選" class="chip-row">
            <svg class="funnel" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M22 3H2l8 9.46V19l4 2v-8.54L22 3z"></path></svg>
            <button v-for="[key, label] in filters" :key="key" type="button" class="chip" :aria-pressed="filter === key" @click="pick(key)">{{ label }}<span class="mono">{{ tally[key] }}</span></button>
            <span class="divider" aria-hidden="true"></span>
            <button v-for="[key, label] in lifeFilters" :key="key" type="button" class="chip" :aria-pressed="filter === key" @click="pick(key)">{{ label }}<span class="mono">{{ tally[key] }}</span></button>
          </div>
          <div role="group" aria-label="排序" class="chip-row">
            <span class="label">排序</span>
            <button v-for="[key, label] in sorts" :key="key" type="button" class="chip" :aria-pressed="sort === key" @click="sort = key">{{ label }}</button>
          </div>
        </div>

        <div class="list">
          <div class="cols-head" aria-hidden="true">
            <span>#</span><span>AGENT</span><span>淨已實現 PnL</span><span>倉位</span><span>最近 Run · 下次檢視</span><span>用量</span><span></span>
          </div>
          <AgentRow
            v-for="a in rows" :key="a.id" :agent="a" :rank="rankOf.get(a.id)" :bar="bar(a.pnl ?? 0, pnls)"
            :open="open === a.id" @toggle="open = open === a.id ? null : a.id" @stop="stop(a)"
          />
          <p v-if="!rows.length" class="none">{{ empty[filter] ?? '沒有符合條件的 agent。' }}</p>
        </div>
      </section>

      <section v-if="modelRows.length" aria-label="依模型比較" class="models">
        <div>
          <h2>依模型比較</h2>
          <p class="label">每個 agent 的平均淨已實現 PnL，% 以各帳號的初始資金計。每組 agent 數與 Run 數可能差很多，僅供參考。</p>
        </div>
        <div class="model-table">
          <div class="model-row model-head" aria-hidden="true"><span>模型</span><span>Agents</span><span>平均報酬分佈</span><span>平均 / agent</span><span>合計</span><span>失敗</span></div>
          <div v-for="m in modelRows" :key="m.name" class="model-row">
            <b class="mono name">{{ m.name }}</b>
            <span class="mono count">×{{ m.agents }}</span>
            <span class="track"><i v-if="m.avgPct != null" :data-tone="pnlTone(m.avgPct)" :style="bar(m.avgPct, modelPcts)"></i></span>
            <span class="mono avg" :data-tone="pnlTone(m.avgPnl)">{{ m.avgPct == null ? '—' : `${money(m.avgPnl)} · ${pct(m.avgPct)}` }}</span>
            <span class="mono total" :data-tone="pnlTone(m.total)">{{ money(m.total) }}</span>
            <span class="mono fails" :data-tone="m.failed ? 'warn' : 'mute'">{{ m.failed ? `${m.failed} 失敗` : '—' }}</span>
          </div>
        </div>
      </section>
    </template>
  </article>
</template>

<style scoped>
.dash { container: page / inline-size; gap: 24px; font-size: 14px; line-height: 1.5; }

.top { display: flex; flex-wrap: wrap; align-items: flex-end; justify-content: space-between; gap: 12px 24px; }
.title { display: flex; flex-wrap: wrap; align-items: center; gap: 12px; }
h1 { font-size: 26px; font-weight: 600; letter-spacing: -0.01em; }
.paper { padding: 3px 10px; border-radius: 6px; background: color-mix(in srgb, var(--acc) 16%, transparent); color: var(--acc); font-size: 12px; font-weight: 600; }
.lede { margin-top: 6px; color: var(--dim); }
.pill { display: flex; align-items: center; gap: 8px; padding: 6px 12px; border: 1px solid var(--line); border-radius: 999px; color: var(--dim); font-size: 12px; }
.pill i { width: 8px; height: 8px; border-radius: 50%; background: var(--acc); flex: none; }
.pill i.live { animation: blip 1.2s infinite; }
.label { color: var(--dim); font-size: 13px; }

.summary { display: flex; flex-wrap: wrap; align-items: stretch; gap: 16px; }
.kpis { flex: 0 0 auto; display: grid; grid-template-columns: repeat(3, 172px); gap: 12px; }
.kpi { display: flex; flex-direction: column; justify-content: center; gap: 2px; padding: 12px 16px; background: var(--surf); border: 1px solid var(--line); border-radius: 12px; }
.kpi .label:first-child { letter-spacing: 0.04em; }
.value { font-size: 32px; font-weight: 600; line-height: 1.15; }

.alerts { flex: 1 1 420px; min-width: 0; display: flex; flex-direction: column; gap: 10px; padding: 14px 16px; border: 1px solid color-mix(in srgb, var(--warn) 35%, transparent); border-radius: 12px; background: color-mix(in srgb, var(--warn) 7%, transparent); }
.alert-head { display: flex; gap: 12px; align-items: flex-start; color: var(--warn); }
.alert-head svg { flex: none; margin-top: 2px; }
.alert-head h2 { font-size: 16px; }
.alert { display: flex; flex-wrap: wrap; align-items: baseline; gap: 2px 20px; padding-top: 8px; border-top: 1px solid color-mix(in srgb, var(--warn) 20%, transparent); color: var(--ink); }
.alert:hover { text-decoration: none; }
.alert:hover .why { color: var(--ink); }
.alert b { flex: 0 0 200px; font-size: 14px; overflow-wrap: anywhere; }
.risk { flex: 0 0 190px; font-size: 13px; font-weight: 600; }
.why { flex: 1 1 220px; min-width: 0; color: var(--dim); font-size: 12px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.agents { display: flex; flex-direction: column; gap: 12px; }
.controls { display: flex; flex-wrap: wrap; align-items: center; justify-content: space-between; gap: 12px 24px; }
.chip-row { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.funnel { width: 44px; color: var(--dim); }
.chip { display: flex; align-items: center; gap: 8px; min-height: 44px; padding: 0 16px; border: 1px solid var(--line); border-radius: 999px; background: transparent; color: var(--dim); font-size: 14px; font-weight: 500; }
.chip:hover:not(:disabled) { border-color: var(--line2); background: transparent; }
.chip[aria-pressed='true'], .chip[aria-pressed='true']:hover { border-color: var(--ink); background: var(--ink); color: var(--bg); }
.chip .mono { font-size: 12px; }
.divider { width: 1px; height: 24px; margin: 0 4px; background: var(--line); }

.list {
  container: agents / inline-size;
  --cols: 32px minmax(150px, 190px) 170px minmax(250px, 1.4fr) minmax(190px, 1fr) 96px 16px;
  background: var(--surf); border: 1px solid var(--line); border-radius: 12px; overflow: hidden;
}
.cols-head { display: grid; grid-template-columns: var(--cols); gap: 16px; padding: 12px 20px; border-bottom: 1px solid var(--line); color: var(--dim); font-size: 12px; letter-spacing: 0.04em; }
.none { padding: 48px 20px; text-align: center; color: var(--dim); }
@container agents (max-width: 1039px) { .cols-head { display: none; } }

.models { display: flex; flex-direction: column; gap: 14px; padding: 20px 20px 8px; background: var(--surf); border: 1px solid var(--line); border-radius: 12px; }
.models h2 { font-size: 16px; }
.models .label { margin-top: 4px; }
.model-table { container: models / inline-size; }
.model-row { display: grid; grid-template-columns: minmax(160px, 1.2fr) 64px minmax(140px, 1.6fr) 170px 100px 72px; gap: 16px; align-items: center; padding: 12px 0; border-top: 1px solid var(--line); }
.model-head { padding: 0 0 8px; border-top: 0; color: var(--dim); font-size: 12px; letter-spacing: 0.04em; }
.model-row .name { font-size: 14px; overflow-wrap: anywhere; }
.model-row .mono:not(.name) { font-size: 13px; }
.count { color: var(--dim); }
.track { position: relative; display: block; height: 8px; border-radius: 4px; background: var(--surf3); }
.track i { position: absolute; top: 0; bottom: 0; border-radius: 4px; background: currentColor; }
@container models (max-width: 719px) {
  .model-head { display: none; }
  .model-row { grid-template-columns: minmax(0, 1fr) auto auto; grid-template-areas: 'name count fails' 'track track track' 'avg avg total'; gap: 8px 12px; }
  .name { grid-area: name; } .count { grid-area: count; } .fails { grid-area: fails; justify-self: end; }
  .track { grid-area: track; } .avg { grid-area: avg; } .total { grid-area: total; justify-self: end; }
}

/* 手機：總覽三格縮小並排，篩選與排序改成可橫向滑動的一列 */
@container page (max-width: 640px) {
  h1 { font-size: 22px; }
  .kpis { flex: 1 1 100%; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 8px; }
  .kpi { padding: 10px 12px; }
  .kpi .label { font-size: 12px; }
  .value { font-size: 22px; }
  .alerts { flex-basis: 100%; }
  .alert b, .risk { flex-basis: auto; }
  .controls { flex-direction: column; align-items: stretch; gap: 8px; }
  .chip-row { flex-wrap: nowrap; overflow-x: auto; scrollbar-width: none; margin: 0 calc(var(--pad) * -1); padding: 0 var(--pad); }
  .chip-row::-webkit-scrollbar { display: none; }
  .chip { flex: none; min-height: 40px; padding: 0 14px; }
  .funnel { flex: none; width: 24px; }
  .models { padding: 16px 16px 4px; }
}
</style>
