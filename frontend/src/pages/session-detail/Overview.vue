<script setup>
import { computed } from 'vue'

import { confirm } from '../../shared/confirm.js'
import { duration, fmtTime, money, num, pnlTone, tone } from '../../shared/format.js'
import { say } from '../../shared/toast.js'
import { deleteEvent } from './api.js'

const props = defineProps({ session: Object, account: Object, runs: Array })
const emit = defineEmits(['goRun', 'changed'])

const events = computed(() => props.session.active_events ?? [])
// params 依 type 壓成一行：timer 顯示 at；價格類顯示 symbol 與門檻
function describe(e) {
  const p = e.params ?? {}
  if (e.type === 'timer') return `at ${fmtTime(p.at)}`
  if (e.type === 'position_close') return `position ${p.position_id}${e.state?.position ? ` · observed ${e.state.position.symbol}` : ' · awaiting observation'}`
  if (e.type === 'price_change_pct') return `${p.market}:${p.symbol} ${p.direction} ${p.pct}% · base ${num(p.base_price)}`
  return `${p.market}:${p.symbol} ${num(p.price)}${e.state ? ` · prev ${num(e.state.price)}` : ''}`
}
async function removeEvent(e) {
  const ok = await confirm({
    title: `刪除 Event ${e.type}？`,
    ok: '確認刪除',
    lines: ['Agent 不會收到通知，也不會自動重建這個 Event', '它要等下次醒來才可能自己重新判斷', '已觸發／過期／刪除的 Event 只留在 event_log，這裡就看不到了'],
  })
  if (!ok) return
  try {
    await deleteEvent(props.session.id, e.id)
    say(`已刪除 Event ${e.id}`)
  } catch (err) {
    say(err.message, 'neg')
  }
  emit('changed')
}

// 待定 6：equity 等四個數字只有 Account Token 自查拿得到，用該帳號的 token 代查
const summary = computed(() => props.session.trading?.account)

const config = computed(() => [
  ['name', props.session.name],
  ['account_id', props.session.account_id],
  ['model_name', `${props.session.model_name}（待定 3：合法值來自 LLM Server /v1/models）`],
  ['model_level', props.session.model_level ?? '—'],
  ['max_loop', String(props.session.max_loop)],
  ['max_tool_call', String(props.session.max_tool_call)],
  ['prompt', props.session.prompt || '（留空 → 使用 Server Default）'],
])
const last = computed(() => props.session.last_run)
const rc = computed(() => props.session.restart_context)
</script>

<template>
  <div class="cols">
    <section class="card config">
      <div class="eyebrow">設定（建立後不可改 · A-11）</div>
      <dl class="kv">
        <template v-for="[k, v] in config" :key="k"><dt>{{ k }}</dt><dd>{{ v }}</dd></template>
      </dl>
    </section>

    <div class="col middle">
      <section class="card">
          <div class="row"><span class="eyebrow">帳戶摘要 · account-wide</span></div>
        <div v-if="account?.error" class="empty">帳號 {{ session.account_id }} 讀不到：{{ account.error.message }}</div>
        <template v-else-if="summary">
          <div class="stats">
            <div v-for="k in ['equity', 'available', 'locked_margin', 'unrealized_pnl']" :key="k" class="stat">
              <small>{{ k }}</small>
              <b :data-tone="k === 'unrealized_pnl' ? pnlTone(summary[k]) : k === 'locked_margin' ? 'dim' : null">{{ k === 'unrealized_pnl' ? money(summary[k]) : num(summary[k], 2) }}</b>
            </div>
          </div>
          <p class="note sep">帳號 <RouterLink :to="{ name: 'accounts', query: { id: session.account_id } }">{{ summary.user_name }}</RouterLink> · 由 Agent Server 取得即時 Backend 資料，憑證不回傳。</p>
        </template>
        <div v-else class="empty">{{ session.trading?.errors?.account?.message ?? '帳戶資料暫不可用' }}</div>
      </section>

      <section class="card">
        <div class="row"><span class="eyebrow">重啟脈絡</span><span class="tag pending">待定 7</span></div>
        <p v-if="rc" class="body" data-tone="dim">
          上次 stop 時 <a href="#" @click.prevent="$emit('goRun', rc.previous_run_id)">Run #{{ rc.previous_run_id }}</a> 的狀態是 <span :data-tone="tone(rc.status)">{{ rc.status }}</span>{{ rc.reason ? `（${rc.reason}）` : '' }}。
          <template v-if="rc.summary">上次摘要：{{ rc.summary }}。</template>
          當時清掉 {{ rc.cleared_events?.length ?? 0 }} 個 active Event<template v-if="rc.cleared_events?.length">（<code v-for="e in rc.cleared_events" :key="e.id">{{ e.type }} </code>）</template>、{{ rc.pending_triggers?.length ?? 0 }} 個排隊中的 trigger。
          下次 start 會以 <code>session_restart</code> 把這份 restart_context 交給 Agent，不會自動重建。
        </p>
        <p v-else class="note">還沒有 stop 過，沒有重啟脈絡。</p>
      </section>
    </div>

    <section class="card last">
      <div class="eyebrow">上一個 Run 摘要</div>
      <template v-if="last">
        <div class="row">
          <a href="#" class="mono" @click.prevent="$emit('goRun', last.run_id)">#{{ last.run_id }}</a>
          <span class="mono small" :data-tone="tone(last.status)">{{ last.status }}</span>
          <span class="mono" data-tone="mute">{{ duration(last.start_time, last.end_time) }} · {{ last.tool_call_count }} tool</span>
        </div>
        <p class="body" data-tone="dim">{{ last.summary ?? last.error ?? '（進行中）' }}</p>
      </template>
      <p v-else class="note">還沒有任何 Run。</p>
      <div class="eyebrow sep">Active Events {{ events.length }} / 10</div>
      <div v-if="events.length" class="rows">
        <div v-for="e in events" :key="e.id" class="row event">
          <span class="mono" data-tone="acc">{{ e.type }}</span>
          <span class="mono grow ellipsis" data-tone="dim" :title="JSON.stringify(e.params)">{{ describe(e) }}</span>
          <a href="#" class="mono small" :title="`expires_at ${fmtTime(e.expires_at)}`" @click.prevent="$emit('goRun', e.created_run_id)">#{{ e.created_run_id }}</a>
          <button class="text" data-tone="neg" @click="removeEvent(e)">刪除</button>
        </div>
      </div>
      <p v-else class="note">沒有 active Event；Agent 在 Run 裡用 create_event 建立後會出現在這裡。</p>
      <p class="note">USER 不能建立 Event（A-35）；唯一的手動觸發是 stop 再 start。已觸發／過期的只在 Run 歷史（triggers）看得到。</p>
    </section>
  </div>
</template>

<style scoped>
.config {
  flex: 1 1 300px;
}
.middle {
  flex: 1 1 260px;
}
.last {
  flex: 1 1 240px;
}
.card {
  display: flex;
  flex-direction: column;
  gap: 9px;
}
.body {
  font-size: 12.5px;
}
.event {
  flex-wrap: nowrap;
  gap: 7px;
}
</style>
