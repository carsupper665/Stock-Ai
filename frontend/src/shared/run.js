import { fmtClock } from './format.js'

// Display server-owned activity; trace remains a separate audit view.

export function activity(run, modelName) {
  return run?.current_activity ?? (run?.status === 'running' ? `執行中（${modelName}）` : '等待 Event')
}

const retry = {
  MAX_LOOP_EXCEEDED: '超過 max_loop；這類額度失敗不能原地重試。',
  MAX_TOOL_CALL_EXCEEDED: '超過 max_tool_call；這類額度失敗不能原地重試。',
  PROVIDER_TIMEOUT: 'LLM Provider 逾時；可用 Retry 建立新的人工重試 Run。',
  RATE_LIMITED: 'LLM Provider 限流；稍後可用 Retry 建立新的人工重試 Run。',
  PROVIDER_UNAVAILABLE: 'LLM Provider 連不上；確認 Provider 後可用 Retry。',
  HARD_STOP: '被使用者 stop 打斷（interrupted）。',
  PERSISTENCE_ERROR: 'Agent Server 持久化失敗，Run 中止；需要檢查 Agent Server。',
}

export function explainError(error) {
  if (!error) return null
  const [code] = error.split(':')
  const message = error.slice(code.length + 1).trim()
  const inner = message.split(':')[0].trim()
  return { code: code.trim(), message, hint: retry[code.trim()] ?? retry[inner] ?? '' }
}

function scalar(value) {
  if (value == null) return '—'
  if (typeof value === 'boolean') return value ? 'true' : 'false'
  return String(value)
}

function fields(value, label = '結果', output = []) {
  if (typeof value === 'string' && /^[\[{]/.test(value.trim())) {
    try {
      const parsed = JSON.parse(value)
      if (parsed && typeof parsed === 'object') return fields(parsed, label, output)
    } catch {
      // The value is ordinary text that happens to begin with JSON punctuation.
    }
  }
  if (Array.isArray(value)) {
    if (!value.length) output.push({ label, value: '（空）' })
    else value.forEach((item, index) => fields(item, `${label} [${index + 1}]`, output))
    return output
  }
  if (value && typeof value === 'object') {
    const entries = Object.entries(value)
    if (!entries.length) output.push({ label, value: '（空）' })
    else entries.forEach(([key, item]) => fields(item, label === '結果' ? key : `${label} · ${key}`, output))
    return output
  }
  output.push({ label, value: scalar(value) })
  return output
}

function explicitReasoning(entry) {
  const value = entry.reasoning ?? entry.reasoning_content
  // Codex reasoning summaries are Markdown with **bold** titles; the text is shown plain.
  return typeof value === 'string' && value.trim() ? value.replace(/\*\*(.+?)\*\*/g, '$1') : ''
}

function finalEnvelope(content) {
  if (typeof content !== 'string' || content.trim()[0] !== '{') return null
  try {
    const value = JSON.parse(content)
    if (!value || typeof value !== 'object' || Array.isArray(value) || typeof value.output !== 'string' || typeof value.summary !== 'string') return null
    return fields({
      status: value.status ?? 'completed',
      output: value.output,
      summary: value.summary,
    })
  } catch {
    return null
  }
}

function modelAttemptError(error) {
  if (typeof error !== 'string' || !error.trim()) return null
  const code = error.split(':', 1)[0].trim()
  return { code: /^[A-Z][A-Z0-9_]{1,63}$/.test(code) ? code : 'MODEL_ATTEMPT_FAILED', message: '模型呼叫失敗', detail: error.trim() }
}

const toolLabels = {
  get_account: '查帳戶', get_positions: '查持倉', get_orders: '查訂單', get_order: '查單筆訂單', get_trades: '查成交', get_ledger: '查帳戶事件',
  get_market_snapshot: '查即時價', get_ohlcv: '查 K 線', get_market_info: '查市場資訊',
  place_order: '下單', cancel_order: '撤單', close_position: '平倉', set_position_stops: '設定停損停利',
  create_event: '建立 Event', delete_event: '刪除 Event', list_events: '查 Event',
  get_chat: '讀群聊', post_chat: '發群聊訊息', get_messages: '讀留言板', post_message: '發留言',
  memory_list: '翻記憶櫃', memory_get: '讀記憶', memory_save: '存記憶', memory_edit: '改記憶', get_run_history: '讀 Run 歷史',
}
const actionTools = new Set(['place_order', 'cancel_order', 'close_position', 'set_position_stops', 'create_event', 'delete_event', 'post_chat', 'post_message', 'memory_save', 'memory_edit'])

function toolItem(entry, key, running) {
  const ok = entry.result?.ok === true
  const error = entry.result?.error
  const status = entry.type === 'tool_denied' ? 'denied' : !entry.ended_at && running ? 'running' : ok ? 'ok' : 'error'
  const posted = entry.tool_name === 'post_chat' || entry.tool_name === 'post_message'
  return {
    key, name: entry.tool_name, label: toolLabels[entry.tool_name] ?? entry.tool_name, action: actionTools.has(entry.tool_name), status,
    duration: Number.isFinite(entry.duration_ms) ? entry.duration_ms : null,
    args: entry.arguments == null ? [] : fields(entry.arguments),
    result: status === 'running' ? [] : ok ? fields(entry.result?.result) : fields(error ? { code: error.code, message: error.message, outcome: error.outcome } : { error: entry.error || '未知錯誤' }),
    // 發到群聊的話直接顯示成聊天氣泡
    chat: posted ? { content: entry.arguments?.content ?? '', tags: entry.arguments?.tags ?? [], error: status === 'error' || status === 'denied' ? error?.code ?? 'ERROR' : null } : null,
    failure: status === 'error' || status === 'denied' ? `${entry.tool_name}\u0000${error?.code ?? entry.error ?? ''}` : null,
    repeated: 1,
  }
}

// 同一個工具連續以同樣的錯誤失敗，只留第一列並記次數
function addTool(owner, tool) {
  const last = owner.tools.at(-1)
  if (tool.failure && last?.failure === tool.failure) last.repeated++
  else owner.tools.push(tool)
}

export const wakeMeanings = { session_start: '第一次啟動', session_restart: '停止後重新啟動', server_recovery: 'Server 重啟後恢復', manual_retry: '使用者對失敗的 Run 重試', event: 'Event 觸發' }

export function matchedText(t) {
  const m = t.matched ?? {}
  if (t.type === 'timer') return `at ${fmtClock(m.at)}`
  if (t.type === 'position_close') return [`position ${m.position_id}`, m.symbol, m.product, m.side, m.close_reason ? `reason ${m.close_reason}` : null].filter(Boolean).join(' · ')
  const parts = [m.condition, m.price != null ? `price ${m.price}` : null, m.change_pct != null ? `change ${m.change_pct.toFixed(2)}%` : null]
  return parts.filter(Boolean).join(' · ')
}

// 喚醒訊息只列重點：為什麼醒來、帳戶、tag 它的群聊訊息；完整內容另外展開
// 沒記錄喚醒訊息的舊 Run 仍列出喚醒原因
function wakeItem(run) {
  const recorded = run?.initial_context && typeof run.initial_context === 'object'
  if (!recorded && !run?.trigger) return null
  const context = recorded ? run.initial_context : {}
  const triggers = Array.isArray(run.triggers) && run.triggers.length ? run.triggers : null
  const inbox = context.inbox ?? null
  const snapshot = context.account_snapshot?.ok ? context.account_snapshot : null
  return {
    key: 'wake', kind: 'wake', side: 'in',
    reasons: triggers
      ? triggers.map((t) => ({ type: t.type, text: `${matchedText(t)} · 由 Run #${t.created_run_id} 建立 · ${fmtClock(t.fired_at)}` }))
      : [{ type: run.trigger, text: wakeMeanings[run.trigger] ?? '' }],
    account: snapshot ? `${snapshot.user_name} · balance ${snapshot.balance}` : null,
    inbox: !recorded ? null : inbox?.error ? { error: inbox.error } : { messages: inbox?.messages ?? [], more: !!inbox?.has_more },
    full: recorded ? JSON.stringify(context, null, 2) : '',
  }
}

function mentionItem(entry, key) {
  let delivered = null
  try { delivered = JSON.parse(entry.content ?? '') } catch { /* 內容不是 JSON 時只顯示原文 */ }
  return { key, kind: 'mention', side: 'in', at: entry.started_at, messages: delivered?.messages ?? [], raw: delivered ? '' : entry.content ?? '' }
}

// 右下角的 context 指示器：最近一次送給模型的內容佔 context window 多少。Provider 回報的
// input_tokens 已含快取，加上 output 就是下一次請求的起點；CLI Harness 的用量是多輪累計，
// 超過 window 時改用 Agent Server 的估計值。
export function contextUse(run) {
  const calls = (run?.progress ?? []).filter((entry) => entry.type === 'model_call' && entry.context_window)
  const last = calls.at(-1)
  if (!last) return null
  const window = last.context_window
  const reported = last.usage?.input_tokens != null ? last.usage.input_tokens + (last.usage.output_tokens ?? 0) : null
  const estimated = reported == null || reported > window
  const tokens = estimated ? last.context_tokens ?? 0 : reported
  const percent = Math.min(100, Math.round((tokens / window) * 100))
  const compactions = (run.progress ?? []).filter((entry) => entry.type === 'context_compaction').length
  return { tokens, window, percent, estimated, warn: percent >= 80, compactions }
}

export function shortTokens(value) {
  return value >= 1000 ? `${(value / 1000).toFixed(value >= 100000 ? 0 : 1)}K` : String(value)
}

function compactionNotice(entry) {
  const stage = entry.result?.result?.stage
  return stage === 'llm' ? '上下文快滿了，已用模型摘要壓縮較早的步驟' : '上下文快滿了，已移除較早步驟的工具輸出'
}

// One Run reads as a chat: messages brought to the model (wake-up, chat tags, the resumed
// conversation) are on the right, each model decision is a message on the left with the tools it
// called collapsed under it, and retries, repairs and compactions are short notices in between.
// live.calls holds what a still-streaming model call has sent so far, keyed by its progress index;
// once the call ends, the text stored in progress is what is shown.
export function conversation(run, live) {
  const running = run?.status === 'running'
  const items = []
  const byCallID = new Map()
  let lastModel = null
  ;(run?.progress ?? []).forEach((entry, index) => {
    if (entry.type === 'model_call') {
      const terminal = entry.attempt_type === 'terminal'
      const error = modelAttemptError(entry.error)
      const envelope = finalEnvelope(entry.content)
      const streamed = entry.ended_at ? null : live?.calls?.[index]
      const content = entry.content ?? streamed?.content
      const model = {
        key: index, kind: 'model', side: 'llm', terminal,
        title: terminal ? '收尾' : `決策 ${entry.iteration}`,
        meta: [entry.model_name, entry.attempt > 1 ? `第 ${entry.attempt} 次嘗試` : null].filter(Boolean).join(' · '),
        reasoning: explicitReasoning(entry) || explicitReasoning(streamed ?? {}),
        content: envelope ? '' : typeof content === 'string' ? content.trim() : '',
        envelope, error,
        thinking: running && !entry.ended_at && !error,
        tools: [], calls: (entry.tool_calls ?? []).length,
      }
      for (const call of entry.tool_calls ?? []) if (call.id) byCallID.set(call.id, model)
      items.push(model)
      lastModel = model
      return
    }
    if (entry.type === 'tool_call' || entry.type === 'tool_denied') {
      const owner = byCallID.get(entry.tool_call_id) ?? lastModel
      const tool = toolItem(entry, index, running)
      if (owner) {
        // 只呼叫一個工具、沒有文字的決策又以同樣錯誤失敗時，併進上一個決策
        const previous = items.at(-2)
        const bare = owner.calls === 1 && owner.tools.length === 0 && !owner.content && !owner.envelope && !owner.error
        if (bare && tool.failure && items.at(-1) === owner && previous?.kind === 'model' && previous.tools.at(-1)?.failure === tool.failure) {
          previous.tools.at(-1).repeated++
          items.pop()
          lastModel = previous
          if (entry.tool_call_id) byCallID.set(entry.tool_call_id, previous)
        } else addTool(owner, tool)
      } else items.push({ key: index, kind: 'notice', side: 'notice', tone: tool.status === 'ok' ? null : 'neg', text: `${tool.label} · ${tool.name}` })
      return
    }
    if (entry.type === 'conversation_resume') {
      const detail = entry.result?.result ?? {}
      items.push({ key: index, kind: 'resume', side: 'in', fromRun: detail.from_run_id, messages: detail.messages, tokens: detail.estimated_tokens })
      return
    }
    if (entry.type === 'chat_mention') {
      items.push(mentionItem(entry, index))
      return
    }
    if (entry.type === 'context_compaction') {
      items.push({ key: index, kind: 'notice', side: 'notice', tone: entry.error ? 'warn' : null, text: compactionNotice(entry) })
      return
    }
    if (entry.type === 'instruction') {
      items.push({ key: index, kind: 'notice', side: 'notice', tone: 'warn', text: '要求模型不再呼叫工具，直接給出最終結果' })
      return
    }
    const error = modelAttemptError(entry.error)
    if (error) items.push({ key: index, kind: 'notice', side: 'notice', tone: 'neg', text: `${error.code} · ${error.message}`, detail: error.detail })
  })
  const wake = wakeItem(run)
  if (wake) items.splice(items[0]?.kind === 'resume' ? 1 : 0, 0, wake)
  return items
}
