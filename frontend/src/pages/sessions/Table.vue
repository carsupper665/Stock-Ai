<script setup>
import { abbr, money, num, pnlTone, relTime, tone } from '../../shared/format.js'

defineProps({ rows: Array, now: Number, sortKey: String, sortDir: Number })
defineEmits(['open', 'toggle', 'sort'])

const cols = [
  ['SESSION', 'name'], ['STATUS'], ['MODEL'], ['CURRENT'], ['RUNS', 'runs', 'num'], ['TOOLS', null, 'num'],
  ['PNL', 'pnl', 'num'], ['POS/ORD', null, 'num'], ['LAST RUN', 'last'], ['上一個 RUN 摘要'], ['', null, 'num'],
]
</script>

<template>
  <div class="card flush">
    <table>
      <thead>
        <tr>
          <th v-for="[label, key, cls] in cols" :key="label" :class="cls" :data-sort="key" @click="key && $emit('sort', key)">
            {{ label }}{{ key === sortKey ? (sortDir === -1 ? ' ↓' : ' ↑') : '' }}
          </th>
        </tr>
      </thead>
      <tbody>
        <tr v-for="s in rows" :key="s.id" data-click @click="$emit('open', s)">
          <td>
            <div class="row name">
              <i class="dot" :class="{ blip: s.status === 'running' }" :data-tone="tone(s.status)"></i>
              <b>{{ s.name }}</b>
            </div>
            <div class="mono small" data-tone="mute">{{ s.id }}</div>
          </td>
          <td><span class="tag" :data-tone="tone(s.status)">{{ s.status }}</span></td>
          <td>
            <div>{{ s.model_name }}</div>
            <div class="mono small" data-tone="mute">level {{ s.model_level ?? '—' }}</div>
          </td>
          <td class="mono" data-tone="acc">
            <div>{{ s.current ? `#${s.current.run_id}` : '—' }}</div>
            <div v-if="s.current" class="small" data-tone="mute">decision {{ s.current.decision_count }}/{{ s.max_loop }} · {{ s.current.model_call_count }} model attempts</div>
          </td>
          <td class="num">{{ s.runCount == null ? '—' : num(s.runCount) + (s.runCount === 100 ? '+' : '') }}</td>
          <td class="num">{{ abbr(s.tools) }}</td>
          <td class="num" :data-tone="s.pnl == null ? 'mute' : pnlTone(s.pnl)">{{ money(s.pnl) }}</td>
          <td class="num" data-tone="dim">{{ s.positions?.length ?? '—' }} / {{ s.orders ?? '—' }}</td>
          <td class="mono" data-tone="mute">{{ relTime(s.lastAt, now) }}</td>
          <td class="summary"><div class="ellipsis" data-tone="dim">{{ s.last ? `#${s.last.run_id} ${s.last.status}${s.last.summary ? ' · ' + s.last.summary : ''}` : '—' }}</div></td>
          <td class="num">
            <button v-if="s.status !== 'deleted'" :data-tone="s.status === 'running' ? 'neg' : 'acc'" @click.stop="$emit('toggle', s)">{{ s.status === 'running' ? 'stop' : 'start' }}</button>
          </td>
        </tr>
      </tbody>
    </table>
  </div>
</template>

<style scoped>
table {
  min-width: 1100px;
}
.name {
  flex-wrap: nowrap;
  gap: 7px;
}
.summary {
  max-width: 300px;
}
.summary div {
  font-size: 12px;
}
</style>
