import { abbr, fmtClock, fmtTime, num } from '../../shared/format.js'

// Session 聚合 → 監控面板的一列。後端沒給的值一律留 null，不推算。
export function toAgent(s) {
  const trading = s.trading ?? {}
  const account = trading.account ?? null
  const initial = account?.initial_balance > 0 ? account.initial_balance : null
  const pnl = trading.pnl?.net_realized ?? null
  const last = s.last_run ?? null
  return {
    id: s.id,
    name: s.name,
    status: s.status,
    model: s.model_name,
    level: s.model_level ?? null,
    pnl,
    pct: pnl != null && initial ? (pnl / initial) * 100 : null,
    equity: account?.equity ?? null,
    positions: trading.positions ?? null,
    positionsError: trading.errors?.positions?.message ?? null,
    last,
    failed: last?.status === 'failed',
    executing: !!s.current_run,
    message: (last?.status === 'failed' ? last.error : last?.summary ?? last?.output) ?? '',
    next: s.active_events ?? [],
    runs: s.total_runs ?? 0,
    // 還沒呼叫過模型時後端給 null，那就是 0；呼叫過但都沒回報用量才是未知
    tokens: s.usage?.total_tokens ?? (s.usage?.model_calls === 0 ? 0 : null),
  }
}

const sameDay = (iso) => new Date(iso).toDateString() === new Date().toDateString()
const crossings = { price_above: '≥', price_below: '≤', price_cross_above: '向上穿越', price_cross_below: '向下穿越' }

export function eventLabel(e) {
  const p = e.params ?? {}
  if (e.type === 'timer') return sameDay(p.at) ? fmtClock(p.at) : fmtTime(p.at)
  if (e.type === 'position_close') return '等待平倉'
  if (e.type === 'price_change_pct') return `${p.symbol} ${p.direction === 'up' ? '漲' : '跌'} ${p.pct}%`
  if (crossings[e.type]) return `${p.symbol} ${crossings[e.type]} ${num(p.price)}`
  return e.type
}

// 計時器依時間先後排前面，其餘照建立順序；最多列兩個，其餘以 +N 表示
export function nextReview(events) {
  const at = (e) => (e.type === 'timer' ? new Date(e.params?.at).getTime() : Infinity)
  const labels = [...events].sort((a, b) => at(a) - at(b)).map(eventLabel)
  if (!labels.length) return '—'
  return labels.slice(0, 2).join(' · ') + (labels.length > 2 ? ` +${labels.length - 2}` : '')
}

export const positionCount = (a) => a.positions?.length ?? null
export const isLive = (a) => a.status === 'running'

export function filterAgents(agents, filter) {
  if (filter === 'stopped' || filter === 'deleted') return agents.filter((a) => a.status === filter)
  const live = agents.filter(isLive)
  if (filter === 'failed') return live.filter((a) => a.failed)
  if (filter === 'holding') return live.filter((a) => positionCount(a) > 0)
  if (filter === 'flat') return live.filter((a) => positionCount(a) === 0)
  return live
}

const byPnl = (a, b) => (b.pnl ?? -Infinity) - (a.pnl ?? -Infinity)
const urgency = (a) => (a.failed && positionCount(a) > 0 ? 0 : a.failed ? 1 : 2)
const sorters = {
  pnl: byPnl,
  urgent: (a, b) => urgency(a) - urgency(b) || (a.pnl ?? Infinity) - (b.pnl ?? Infinity),
  runs: (a, b) => b.runs - a.runs,
  tokens: (a, b) => (b.tokens ?? 0) - (a.tokens ?? 0) || byPnl(a, b),
}
export const sortAgents = (agents, sort) => [...agents].sort(sorters[sort])

// 已刪除的不參與排名；PnL 未知的沒有名次
export function ranks(agents) {
  const ranked = agents.filter((a) => a.status !== 'deleted' && a.pnl != null).sort(byPnl)
  return new Map(ranked.map((a, i) => [a.id, i + 1]))
}

export function counts(agents) {
  const live = agents.filter(isLive)
  return {
    running: live.length,
    failed: live.filter((a) => a.failed).length,
    holding: live.filter((a) => positionCount(a) > 0).length,
    flat: live.filter((a) => positionCount(a) === 0).length,
    stopped: agents.filter((a) => a.status === 'stopped').length,
    deleted: agents.filter((a) => a.status === 'deleted').length,
  }
}

export function overview(agents) {
  const live = agents.filter(isLive)
  const failed = live.filter((a) => a.failed)
  const holding = live.filter((a) => positionCount(a) > 0)
  const positions = holding.flatMap((a) => a.positions)
  const unknown = live.filter((a) => positionCount(a) == null).length
  const reported = live.filter((a) => a.tokens != null)
  const runs = reported.reduce((total, a) => total + a.runs, 0)
  const tokens = reported.reduce((total, a) => total + a.tokens, 0)
  const silent = live.length - reported.length
  return [
    { label: '需要處理', value: `${failed.length} / ${live.length}`, tone: failed.length ? 'warn' : null, sub: `最近一次 Run 失敗 · 其中 ${failed.filter((a) => positionCount(a) > 0).length} 個仍持倉` },
    {
      label: '持倉中', value: `${holding.length} / ${live.length}`, tone: null,
      sub: `多 ${positions.filter((p) => p.side === 'long').length} · 空 ${positions.filter((p) => p.side === 'short').length} 筆${unknown ? ` · ${unknown} 個讀不到` : ''}`,
    },
    { label: '累計 Token', value: abbr(tokens), tone: null, sub: `共 ${runs} 個 Run${runs ? ` · 每 Run 約 ${abbr(Math.round(tokens / runs))}` : ''}${silent ? ` · ${silent} 個沒回報用量` : ''}` },
  ]
}

const errorCode = (message) => message.split(':', 1)[0].trim()

export function alerts(agents) {
  const failed = agents.filter((a) => isLive(a) && a.failed)
  const items = [...failed].sort((a, b) => (positionCount(b) > 0) - (positionCount(a) > 0)).map((a) => ({
    id: a.id,
    name: a.name,
    run: a.last.run_id,
    message: a.message,
    holding: positionCount(a) > 0,
    risk: positionCount(a) > 0 ? `持倉 ${positionCount(a)} 筆 · 倉位可能無人盯盤` : positionCount(a) === 0 ? '無持倉，無倉位風險' : '倉位讀不到',
  }))
  const codes = new Set(failed.map((a) => errorCode(a.message)))
  const cause = failed.length > 1 && codes.size === 1 && [...codes][0] ? `錯誤代碼都是 ${[...codes][0]}，可能是同一個 Provider 或 CLI 設定問題。` : null
  return { items, cause }
}

// 長條以 0 為基準，左右跨度取所有值與 0 的範圍；太短的補到 1.5% 才看得見
export function bar(value, values) {
  const lo = Math.min(0, ...values)
  const hi = Math.max(0, ...values)
  const span = hi - lo || 1
  const zero = ((0 - lo) / span) * 100
  const width = Math.max((Math.abs(value) / span) * 100, 1.5)
  const left = value < 0 ? Math.max(zero - width, 0) : Math.min(zero, 100 - width)
  return { left: `${left.toFixed(2)}%`, width: `${width.toFixed(2)}%` }
}

export function models(agents) {
  const groups = new Map()
  for (const a of agents.filter((agent) => agent.status !== 'deleted')) {
    if (!groups.has(a.model)) groups.set(a.model, [])
    groups.get(a.model).push(a)
  }
  const mean = (list, pick) => (list.length ? list.reduce((total, a) => total + pick(a), 0) / list.length : null)
  const rows = [...groups].map(([name, list]) => {
    const known = list.filter((a) => a.pct != null)
    return {
      name,
      agents: list.length,
      avgPct: mean(known, (a) => a.pct),
      avgPnl: mean(known, (a) => a.pnl),
      total: known.length ? known.reduce((total, a) => total + a.pnl, 0) : null,
      failed: list.filter((a) => a.failed).length,
    }
  })
  return rows.sort((a, b) => (b.avgPct ?? -Infinity) - (a.avgPct ?? -Infinity))
}
