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
  return typeof value === 'string' && value.trim() ? value : ''
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
  return { code: /^[A-Z][A-Z0-9_]{1,63}$/.test(code) ? code : 'MODEL_ATTEMPT_FAILED', message: '模型呼叫失敗；完整錯誤保留於 Run 稽核資料。' }
}

const toolLabels = {
  get_account: '查帳戶', get_positions: '查持倉', get_orders: '查訂單', get_order: '查單筆訂單', get_trades: '查成交', get_ledger: '查帳戶事件',
  get_market_snapshot: '查即時價', get_ohlcv: '查 K 線', get_market_info: '查市場資訊',
  place_order: '下單', cancel_order: '撤單', close_position: '平倉', set_position_stops: '設定停損停利',
  create_event: '建立 Event', delete_event: '刪除 Event', list_events: '查 Event',
  get_messages: '讀留言板', post_message: '發留言',
  memory_list: '翻記憶櫃', memory_get: '讀記憶', memory_save: '存記憶', memory_edit: '改記憶', get_run_history: '讀 Run 歷史',
}
const actionTools = new Set(['place_order', 'cancel_order', 'close_position', 'set_position_stops', 'create_event', 'delete_event', 'post_message', 'memory_save', 'memory_edit'])

function toolItem(entry, key, running) {
  const ok = entry.result?.ok === true
  const error = entry.result?.error
  const status = entry.type === 'tool_denied' ? 'denied' : !entry.ended_at && running ? 'running' : ok ? 'ok' : 'error'
  return {
    key, name: entry.tool_name, label: toolLabels[entry.tool_name] ?? entry.tool_name, action: actionTools.has(entry.tool_name), status,
    duration: Number.isFinite(entry.duration_ms) ? entry.duration_ms : null,
    args: entry.arguments == null ? [] : fields(entry.arguments),
    result: status === 'running' ? [] : ok ? fields(entry.result?.result) : fields(error ? { code: error.code, message: error.message, outcome: error.outcome } : { error: entry.error || '未知錯誤' }),
  }
}

function compactionNotice(entry) {
  const stage = entry.result?.result?.stage
  return stage === 'llm' ? '上下文快滿了，已用模型摘要壓縮較早的步驟' : '上下文快滿了，已移除較早步驟的工具輸出'
}

// One Run reads as a conversation: each model decision is a message, the tools it called are
// collapsed under it, and retries, repairs and compactions are short notices between them.
export function conversation(run) {
  const running = run?.status === 'running'
  const items = []
  const byCallID = new Map()
  let lastModel = null
  ;(run?.progress ?? []).forEach((entry, index) => {
    if (entry.type === 'model_call') {
      const terminal = entry.attempt_type === 'terminal'
      const error = modelAttemptError(entry.error)
      const envelope = finalEnvelope(entry.content)
      const model = {
        key: index, kind: 'model', terminal,
        title: terminal ? '收尾' : `決策 ${entry.iteration}`,
        meta: [entry.model_name, entry.attempt > 1 ? `第 ${entry.attempt} 次嘗試` : null].filter(Boolean).join(' · '),
        reasoning: explicitReasoning(entry),
        content: envelope ? '' : typeof entry.content === 'string' ? entry.content.trim() : '',
        envelope, error,
        thinking: running && !entry.ended_at && !error,
        tools: [],
      }
      for (const call of entry.tool_calls ?? []) if (call.id) byCallID.set(call.id, model)
      items.push(model)
      lastModel = model
      return
    }
    if (entry.type === 'tool_call' || entry.type === 'tool_denied') {
      const owner = byCallID.get(entry.tool_call_id) ?? lastModel
      const tool = toolItem(entry, index, running)
      if (owner) owner.tools.push(tool)
      else items.push({ key: index, kind: 'notice', tone: tool.status === 'ok' ? null : 'neg', text: `${tool.label} · ${tool.name}` })
      return
    }
    if (entry.type === 'context_compaction') {
      items.push({ key: index, kind: 'notice', tone: entry.error ? 'warn' : null, text: compactionNotice(entry) })
      return
    }
    if (entry.type === 'instruction') {
      items.push({ key: index, kind: 'notice', tone: 'warn', text: '要求模型不再呼叫工具，直接給出最終結果' })
      return
    }
    const error = modelAttemptError(entry.error)
    if (error) items.push({ key: index, kind: 'notice', tone: 'neg', text: `${error.code} · ${error.message}` })
  })
  return items
}
