<script setup>
import { computed } from 'vue'

import { fmtTime, money, num, pnlTone } from '../../shared/format.js'
import { nextReview } from './agents.js'

const props = defineProps({ agent: Object, rank: Number, bar: Object, open: Boolean })
defineEmits(['toggle', 'stop'])

const a = computed(() => props.agent)
const live = computed(() => a.value.status === 'running')
const pct = (v) => `${v > 0 ? '+' : v < 0 ? '−' : ''}${num(Math.abs(v), 2)}%`
const runLabels = { completed: ['完成', 'dim'], failed: ['失敗', 'warn'], interrupted: ['中斷', 'warn'], running: ['執行中', 'acc'] }
const run = computed(() => (a.value.executing ? runLabels.running : runLabels[a.value.last?.status] ?? [a.value.last?.status ?? '尚無 Run', 'mute']))
const statusText = { running: '執行中', stopped: '已停止', deleted: '已刪除（資料仍保留）' }
const runLink = (runId) => ({ name: 'session-detail', params: { id: a.value.id }, query: { tab: 'runs', run: runId } })

// 只用後端給的欄位；倉位大小沒有標記價時退回均價並標 ≈
const positions = computed(() => (a.value.positions ?? []).map((p) => {
  const price = p.mark_price ?? p.entry_price
  return {
    ...p,
    coin: p.symbol?.replace(/USDT$/, ''),
    size: p.quantity != null && price != null ? `${p.mark_price == null ? '≈ ' : ''}${num(p.quantity * price, 2)} USDT` : '—',
    roe: p.margin > 0 && p.unrealized_pnl != null ? (p.unrealized_pnl / p.margin) * 100 : null,
    stopsAt: [p.stop_loss_source?.run_id && `SL Run #${p.stop_loss_source.run_id}`, p.take_profit_source?.run_id && `TP Run #${p.take_profit_source.run_id}`].filter(Boolean).join(' · '),
  }
}))
</script>

<template>
  <div class="agent" :data-status="agent.status">
    <button type="button" class="head" :aria-expanded="open" :data-failed="live && agent.failed ? '' : null" @click="$emit('toggle')">
      <span class="rank mono">{{ rank ?? '—' }}</span>

      <span class="who">
        <b class="name mono">{{ agent.name }}</b>
        <span v-if="live && agent.failed" class="badge" data-tone="warn">最近一次 Run 失敗</span>
        <span v-if="!live" class="badge" data-tone="mute">{{ agent.status === 'deleted' ? '已刪除' : '已停止' }}</span>
        <span class="mono sub">{{ agent.model }} / {{ agent.level ?? '—' }}</span>
      </span>

      <span class="pnl">
        <span class="figure">
          <b class="mono" :data-tone="pnlTone(agent.pnl)">{{ money(agent.pnl) }}</b>
          <small v-if="agent.pct != null" class="mono" :data-tone="pnlTone(agent.pnl)">{{ pct(agent.pct) }}</small>
        </span>
        <span v-if="agent.pnl != null" class="track"><i :data-tone="pnlTone(agent.pnl)" :style="{ left: bar.left, width: bar.width }"></i></span>
        <span class="mono sub">權益 {{ agent.equity == null ? '—' : `$${num(agent.equity, 2)}` }}</span>
      </span>

      <span class="pos">
        <span v-for="p in positions" :key="p.id" class="mini">
          <span class="line">
            <b class="mono">{{ p.symbol }}</b>
            <span class="side mono" :data-tone="p.side === 'short' ? 'neg' : 'pos'">{{ p.side === 'short' ? '空' : p.side === 'long' ? '多' : p.side }}</span>
            <span v-if="p.leverage" class="lev mono">{{ p.leverage }}x</span>
          </span>
          <span class="nums">
            <span><small>未實現盈虧</small><b class="mono" :data-tone="pnlTone(p.unrealized_pnl)">{{ money(p.unrealized_pnl) }}</b></span>
            <span class="mid"><small>倉位大小</small><span class="mono">{{ p.size }}</span></span>
            <span class="end"><small>回報率</small><b class="mono" :data-tone="p.roe == null ? 'mute' : pnlTone(p.roe)">{{ p.roe == null ? '—' : pct(p.roe) }}</b></span>
          </span>
          <span v-if="p.stop_loss || p.take_profit" class="line">
            <span v-if="p.stop_loss" class="stop mono" data-tone="neg">停損 {{ num(p.stop_loss) }}</span>
            <span v-if="p.take_profit" class="stop mono" data-tone="pos">停利 {{ num(p.take_profit) }}</span>
          </span>
        </span>
        <span v-if="agent.positions && !positions.length" class="mono flat">空手</span>
        <span v-if="!agent.positions" class="flat" data-tone="neg">倉位讀不到{{ agent.positionsError ? `：${agent.positionsError}` : '' }}</span>
      </span>

      <span class="run">
        <span class="mono meta"><span v-if="agent.last">#{{ agent.last.run_id }}</span><b :data-tone="run[1]">{{ run[0] }}</b></span>
        <span v-if="agent.message" class="msg" :data-tone="agent.failed ? 'warn' : null">{{ agent.message }}</span>
        <span class="sub">下次檢視　<span class="mono next">{{ nextReview(agent.next) }}</span></span>
      </span>

      <span class="use mono">
        <span><b>{{ agent.runs }}</b> runs</span>
        <span>{{ agent.tokens == null ? '—' : num(agent.tokens) }} tokens</span>
        <span data-tone="mute">{{ agent.tokens == null || !agent.runs ? '—' : num(Math.round(agent.tokens / agent.runs)) }} / run</span>
      </span>

      <svg class="chev" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="m9 6 6 6-6 6"></path></svg>
    </button>

    <div v-if="open" class="more">
      <div class="detail">
        <span class="label">最近一次 Run 的完整訊息</span>
        <p class="mono full" :data-tone="agent.failed ? 'warn' : null">{{ agent.message || (agent.last ? '這個 Run 沒有留下摘要。' : '還沒有 Run。') }}</p>
        <RouterLink v-if="agent.last" :to="runLink(agent.last.run_id)" class="small">查看 Run #{{ agent.last.run_id }} →</RouterLink>
        <dl v-for="p in positions" :key="p.id" class="facts">
          <div><dt>持倉均價</dt><dd class="mono">{{ num(p.entry_price) }}</dd></div>
          <div><dt>標記價格</dt><dd class="mono">{{ num(p.mark_price) }}</dd></div>
          <div><dt>持倉量</dt><dd class="mono">{{ num(p.quantity) }} {{ p.coin }}</dd></div>
          <div v-if="p.margin > 0"><dt>保證金</dt><dd class="mono">{{ num(p.margin, 2) }}</dd></div>
          <div v-if="p.opened_at"><dt>開倉時間</dt><dd class="mono">{{ fmtTime(p.opened_at) }}</dd></div>
          <div v-if="p.stopsAt"><dt>SL / TP 設定於</dt><dd class="mono">{{ p.stopsAt }}</dd></div>
        </dl>
      </div>
      <div class="side-facts">
        <div><span class="label">狀態</span><span>{{ statusText[agent.status] ?? agent.status }}</span></div>
        <div><span class="label">Session</span><span class="mono id">{{ agent.id }}</span></div>
        <div class="actions">
          <div><span class="label">模型 / effort</span><span class="mono">{{ agent.model }} / {{ agent.level ?? '—' }}</span></div>
          <span class="buttons">
            <RouterLink :to="{ name: 'session-detail', params: { id: agent.id } }" class="button">檢視細節</RouterLink>
            <button v-if="live" type="button" class="button mono" data-tone="neg" @click="$emit('stop')">stop</button>
          </span>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.agent { border-bottom: 1px solid var(--line); }
.agent:last-child { border-bottom: 0; }
.agent[data-status='stopped'], .agent[data-status='deleted'] { opacity: 0.8; }
.agent[data-status='deleted'] .name { text-decoration: line-through; }

/* 寬版：跟表頭共用 --cols；窄版（容器 < 1040px）在下面改成卡片 */
.head {
  display: grid; grid-template-columns: var(--cols); gap: 16px; align-items: start;
  width: 100%; padding: 12px 20px; border: 0; border-radius: 0; background: transparent;
  color: var(--ink); font-size: 14px; text-align: left; white-space: normal;
}
.head[data-failed] { background: color-mix(in srgb, var(--warn) 5%, transparent); }
.head[aria-expanded='true'] { background: var(--surf2); }
.head:focus-visible { outline: 2px solid var(--acc); outline-offset: -2px; }
.head > span { min-width: 0; }

.rank { color: var(--mute); font-size: 13px; padding-top: 2px; }
.who, .pnl, .run { display: flex; flex-direction: column; gap: 6px; }
.name { font-size: 14px; line-height: 1.3; overflow-wrap: anywhere; }
.sub { color: var(--dim); font-size: 12px; }
.mono.sub { font-size: 12px; }
.badge { align-self: flex-start; padding: 2px 8px; border-radius: 6px; font-size: 12px; font-weight: 600; background: color-mix(in srgb, currentColor 16%, transparent); }

.figure { display: flex; align-items: baseline; gap: 8px; }
.figure b { font-size: 18px; }
.figure small { font-size: 12px; }
.track { position: relative; display: block; height: 6px; border-radius: 3px; background: var(--surf3); }
.track i { position: absolute; top: 0; bottom: 0; border-radius: 3px; background: currentColor; }

.pos { display: flex; flex-direction: column; gap: 8px; }
.mini { display: flex; flex-direction: column; gap: 8px; padding: 10px 12px; background: var(--bg); border: 1px solid var(--line); border-radius: 10px; }
.line { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; }
.line b { font-size: 14px; margin-right: 2px; }
.side, .lev, .stop { padding: 1px 8px; border-radius: 4px; font-size: 12px; }
.side { border: 1px solid color-mix(in srgb, currentColor 45%, transparent); background: color-mix(in srgb, currentColor 12%, transparent); font-weight: 600; }
.lev { border: 1px solid var(--line); color: var(--dim); }
.stop { padding: 2px 8px; background: color-mix(in srgb, currentColor 10%, transparent); }
.nums { display: grid; grid-template-columns: 1fr auto 1fr; align-items: end; gap: 12px; }
.nums > span { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.nums small { color: var(--dim); font-size: 12px; }
.nums b { font-size: 18px; line-height: 1.2; }
.nums .mono { font-size: 14px; }
.nums b.mono { font-size: 18px; }
.nums .mid { align-items: center; }
.nums .end { align-items: flex-end; }
.flat { font-size: 13px; color: var(--mute); }
.flat.mono { font-size: 13px; }

.meta { display: flex; gap: 8px; color: var(--dim); font-size: 12px; }
.msg { display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; font-size: 13px; line-height: 1.45; }
.next { color: var(--ink); font-size: 12px; }

.use { display: flex; flex-direction: column; gap: 3px; color: var(--dim); font-size: 12px; }
.use b { color: var(--ink); font-size: 15px; font-weight: 400; }
.chev { color: var(--mute); margin-top: 3px; transition: transform 0.15s; }
.head[aria-expanded='true'] .chev { transform: rotate(90deg); }

.more { display: grid; grid-template-columns: minmax(0, 2fr) minmax(0, 1fr); gap: 24px; padding: 8px 20px 20px 68px; background: var(--surf2); }
.detail, .side-facts { display: flex; flex-direction: column; gap: 12px; min-width: 0; }
.detail { gap: 6px; }
.side-facts > div:not(.actions), .actions > div { display: flex; flex-direction: column; gap: 2px; min-width: 0; }
.label { color: var(--dim); font-size: 12px; }
.full { font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; white-space: pre-wrap; }
.id { overflow-wrap: anywhere; }
.facts { display: flex; flex-wrap: wrap; gap: 12px 32px; margin: 8px 0 0; }
.facts > div { display: flex; flex-direction: column; gap: 2px; }
.facts dt { color: var(--dim); font-size: 12px; }
.facts dd { margin: 0; font-size: 13px; }
.actions { display: flex; flex-flow: row wrap; align-items: flex-end; justify-content: space-between; gap: 12px; }
.buttons { display: flex; gap: 8px; margin-left: auto; }
.button { display: inline-flex; align-items: center; min-height: 44px; padding: 0 16px; border: 1px solid var(--line2); border-radius: 8px; background: transparent; color: var(--ink); font-size: 13px; font-weight: 500; }
.button:hover { text-decoration: none; background: var(--surf3); }
.button[data-tone='neg'] { border-color: color-mix(in srgb, var(--neg) 50%, transparent); color: var(--neg); }

/* 窄版：每個 agent 一張卡；容器夠寬時倉位放右欄 */
@container agents (max-width: 1039px) {
  .head {
    grid-template-columns: 24px minmax(0, 1fr) 16px; gap: 12px;
    grid-template-areas: 'rank who chev' 'pnl pnl pnl' 'pos pos pos' 'run run run' 'use use use';
    padding: 14px 16px;
  }
  .rank { grid-area: rank; } .who { grid-area: who; } .pnl { grid-area: pnl; } .pos { grid-area: pos; }
  .run { grid-area: run; } .use { grid-area: use; } .chev { grid-area: chev; }
  .use { flex-direction: row; flex-wrap: wrap; gap: 4px 14px; }
  .more { grid-template-columns: minmax(0, 1fr); gap: 16px; padding: 4px 16px 16px; }
}
@container agents (min-width: 640px) and (max-width: 1039px) {
  .head {
    grid-template-columns: 24px minmax(0, 1fr) minmax(0, 1.1fr) 16px; column-gap: 20px;
    grid-template-areas: 'rank who pos chev' 'rank pnl pos chev' 'rank run pos chev' 'rank use pos chev';
    grid-template-rows: auto auto auto 1fr;
  }
}
@container agents (max-width: 420px) {
  .nums b, .nums b.mono, .figure b { font-size: 16px; }
  .nums { gap: 8px; }
}
</style>
