<script setup>
import { abbr, mmss, money, num, pnlTone, tone } from '../../shared/format.js'
import { activity } from '../../shared/run.js'

defineProps({ rows: Array, now: Number })
defineEmits(['open', 'toggle'])

const pct = (n, max) => `${Math.min(100, Math.round((n / max) * 100))}%`
const elapsed = (run, now) => mmss(Math.max(0, (now - new Date(run.start_time)) / 1000))
</script>

<template>
  <div class="grid">
    <section v-for="s in rows" :key="s.id" class="card col session" :data-status="tone(s.status)" :data-span="s.status === 'running' ? 2 : null" @click="$emit('open', s)">
      <div class="row head">
        <div class="grow col title">
          <div class="row name">
            <i class="dot" :class="{ blip: s.status === 'running' }" :data-tone="tone(s.status)"></i>
            <b class="ellipsis">{{ s.name }}</b>
            <span class="mono small" :data-tone="tone(s.status)">{{ s.status }}</span>
          </div>
          <span class="mono small ellipsis" data-tone="mute">{{ s.id }} · {{ s.model_name }} / {{ s.model_level ?? '—' }}</span>
        </div>
        <div class="pnl">
          <div class="mono pnl-value" :data-tone="s.pnl == null ? 'mute' : pnlTone(s.pnl)">{{ money(s.pnl) }}</div>
          <div class="mono small" data-tone="mute">帳戶淨已實現 PnL</div>
        </div>
      </div>

      <div v-if="s.status === 'running'" class="inset col current">
        <template v-if="s.current">
          <div class="row mono">
            <span data-tone="acc">#{{ s.current.run_id }}</span>
            <span class="grow ellipsis" data-tone="dim">{{ activity(s.current, s.model_name) }}</span>
            <span data-tone="mute">{{ elapsed(s.current, now) }}</span>
          </div>
          <div class="row bars">
            <div class="grow">
              <div class="row between mono small" data-tone="mute"><span>decision</span><span>{{ s.current.decision_count }} / {{ s.max_loop }} · {{ s.current.model_call_count }} attempts</span></div>
              <div class="bar thin"><i data-tone="acc" :style="{ width: pct(s.current.decision_count, s.max_loop) }"></i></div>
            </div>
            <div class="grow">
              <div class="row between mono small" data-tone="mute"><span>tool</span><span>{{ s.current.tool_call_count }} / {{ s.max_tool_call }}</span></div>
              <div class="bar thin"><i data-tone="warn" :style="{ width: pct(s.current.tool_call_count, s.max_tool_call) }"></i></div>
            </div>
          </div>
        </template>
        <div v-else class="mono" data-tone="dim">running · 沒有進行中的 Run（上一個 Run 已結束，等下一個 Event）</div>
      </div>

      <p class="summary ellipsis" data-tone="dim">
        <template v-if="s.last">#{{ s.last.run_id }} {{ s.last.status }}{{ s.last.summary ? ` · ${s.last.summary}` : s.last.error ? ` · ${s.last.error}` : '' }}</template>
        <template v-else>還沒有任何 Run</template>
      </p>

      <div class="row foot sep mono" data-tone="mute">
        <span>{{ num(s.runCount) }} runs</span>
        <span>{{ abbr(s.tools) }} tool</span>
        <span>{{ s.positions?.length ?? '—' }} pos</span>
        <span v-if="s.trading?.errors?.account || s.trading?.errors?.positions" data-tone="warn">交易資料部分不可用</span>
        <span class="grow"></span>
        <button v-if="s.status !== 'deleted'" :data-tone="s.status === 'running' ? 'neg' : 'acc'" @click.stop="$emit('toggle', s)">{{ s.status === 'running' ? 'stop' : 'start' }}</button>
        <span v-else>—</span>
      </div>
    </section>
  </div>
</template>

<style scoped>
.grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(330px, 1fr));
  gap: 12px;
}
.session {
  gap: 10px;
  cursor: pointer;
}
.session[data-span='2'] {
  grid-column: span 2;
}
@media (max-width: 720px) {
  .session[data-span='2'] {
    grid-column: auto;
  }
}
.head {
  align-items: flex-start;
  flex-wrap: nowrap;
}
.title {
  gap: 2px;
}
.name {
  flex-wrap: nowrap;
  gap: 7px;
}
.name b {
  font-size: 14px;
}
.pnl {
  text-align: right;
  flex: none;
}
.pnl-value {
  font-size: 17px;
  font-weight: 500;
  line-height: 1;
}
.current {
  gap: 7px;
}
.bars {
  flex-wrap: nowrap;
}
.between {
  justify-content: space-between;
}
.summary {
  font-size: 12px;
}
.foot {
  gap: 10px;
}
</style>
