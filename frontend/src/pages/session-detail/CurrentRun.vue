<script setup>
import { computed } from 'vue'
import { mmss } from '../../shared/format.js'
import { activity, contextUse, shortTokens } from '../../shared/run.js'

const props = defineProps({ session: Object, current: Object, now: Number, compact: Boolean })
const usage = computed(() => contextUse(props.current))
const pct = (n, max) => `${Math.min(100, Math.round((n / max) * 100))}%`
</script>

<template>
  <section v-if="session.status === 'running'" class="card row band" :class="{ compact }" data-tone="acc">
    <div class="col grow">
      <div class="eyebrow">當前 Run</div>
      <template v-if="current">
        <div class="row">
          <span class="mono run-id" data-tone="acc">#{{ current.run_id }}</span>
          <span>{{ activity(current, session.model_name) }}</span>
          <span class="mono" data-tone="mute">已跑 {{ mmss(Math.max(0, (now - new Date(current.start_time)) / 1000)) }}</span>
        </div>
        <div class="mono" data-tone="mute">trigger：{{ current.trigger }}</div>
      </template>
      <div v-else class="col">
        <b data-tone="warn">running · 沒有進行中的 Run</b>
        <span class="note">正在等待 Event；Active {{ session.active_event_count }}，Pending {{ session.pending_trigger_count }}。</span>
      </div>
    </div>
    <div class="col bars">
      <div>
        <div class="row between mono small" data-tone="mute"><span>decision / max_loop</span><span>{{ current ? current.decision_count : '—' }} / {{ session.max_loop }}</span></div>
        <div class="bar"><i data-tone="acc" :style="{ width: current ? pct(current.decision_count, session.max_loop) : 0 }"></i></div>
        <div v-if="current" class="mono small" data-tone="mute">模型呼叫 {{ current.model_call_count }} 次（含重試與終結嘗試）</div>
      </div>
      <div>
        <div class="row between mono small" data-tone="mute"><span>tool_call / max_tool_call</span><span>{{ current ? current.tool_call_count : '—' }} / {{ session.max_tool_call }}</span></div>
        <div class="bar"><i data-tone="warn" :style="{ width: current ? pct(current.tool_call_count, session.max_tool_call) : 0 }"></i></div>
      </div>
    </div>
    <div class="stats tokens">
      <div class="stat"><small>context / window</small><b>{{ usage ? `${shortTokens(usage.tokens)} / ${shortTokens(usage.window)}（${usage.percent}%）` : '—' }}</b></div>
    </div>
  </section>
  <section v-else class="card row band" :class="{ compact }" :data-tone="session.status === 'deleted' ? 'mute' : 'warn'">
    <div class="col grow">
      <div class="eyebrow">當前 Run</div>
      <b :data-tone="session.status === 'deleted' ? 'mute' : 'warn'">{{ session.status === 'deleted' ? '已刪除 · 不能 start' : '未執行 · 沒有進行中的 Run' }}</b>
      <span class="note">
        <template v-if="session.status === 'deleted'">資料保留供跨 Session 比較，但不可 restore、不可 start。</template>
        <template v-else>按 start 會以 trigger {{ session.current_run_id ? 'session_restart' : 'session_start' }} 開一個新 Run{{ session.restart_context ? '；Agent 會拿到上次中斷在哪（見概覽的重啟脈絡）' : '' }}。</template>
      </span>
    </div>
    <div class="col limits mono small" data-tone="mute">
      <div class="row between"><span>max_loop</span><span>— / {{ session.max_loop }}</span></div>
      <div class="row between"><span>max_tool_call</span><span>— / {{ session.max_tool_call }}</span></div>
      <span>沒有輪詢在跑（無進行中的 Run）</span>
    </div>
  </section>
</template>

<style scoped>
.band {
  gap: 16px;
  align-items: center;
}
.band > .grow {
  flex: 1 1 260px;
  gap: 6px;
}
.run-id {
  font-size: 20px;
  font-weight: 500;
  line-height: 1;
}
.bars {
  flex: 1 1 200px;
  gap: 8px;
}
.limits {
  flex: 0 1 220px;
  gap: 6px;
}
.between {
  justify-content: space-between;
}
.tokens {
  flex: 1 1 190px;
  grid-template-columns: 1fr;
}
.compact {
  flex-direction: column;
  align-items: stretch;
}
.compact > .grow, .compact .bars, .compact .limits, .compact .tokens {
  flex: initial;
  width: 100%;
  min-width: 0;
}
</style>
