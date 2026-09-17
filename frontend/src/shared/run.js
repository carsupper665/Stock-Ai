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
      output: value.output,
      summary: value.summary,
      candidate_memories: value.candidate_memories ?? [],
      expire_memories: value.expire_memories ?? [],
    })
  } catch {
    return null
  }
}

function modelAttemptError(error) {
  if (typeof error !== 'string' || !error.trim()) return null
  const code = error.split(':', 1)[0].trim()
  return { code: /^[A-Z][A-Z0-9_]{1,63}$/.test(code) ? code : 'MODEL_ATTEMPT_FAILED', message: '模型嘗試失敗；完整錯誤保留於 Run 稽核資料。' }
}

function modelDiagnostics(entry) {
  const values = [
    ['session mode', entry.session_mode, (value) => typeof value === 'string' && value.trim()],
    ['fallback reason', entry.fallback_reason, (value) => typeof value === 'string' && value.trim()],
    ['invocations', entry.invocation_count, (value) => Number.isInteger(value) && value >= 0],
  ]
  return values.filter(([, value, valid]) => valid(value)).map(([label, value]) => ({ label, value: String(value) }))
}

// Arguments are intentionally omitted. Only model text actually returned by the server and
// human-readable tool result fields are projected into the console.
export function steps(run) {
  return (run?.progress ?? []).map((entry, index) => {
    if (entry.type === 'model_call') {
      const terminal = entry.attempt_type === 'terminal'
      const tools = entry.tool_calls?.map((call) => call.name).filter(Boolean) ?? []
      const error = modelAttemptError(entry.error)
      const complete = entry.ended_at || entry.finish_reason || typeof entry.content === 'string' || Array.isArray(entry.tool_calls)
      const envelope = finalEnvelope(entry.content)
      return {
        key: index,
        kind: 'model',
        step: terminal ? 'terminal' : `decision ${entry.iteration}`,
        title: terminal ? '終結嘗試' : '模型決策',
        meta: [entry.attempt ? `${terminal ? '終結' : '模型呼叫'} attempt ${entry.attempt}${terminal ? ' / 4' : ''}` : null, entry.model_name, entry.provider_id && entry.model ? `${entry.provider_id} / ${entry.model}` : null, entry.finish_reason].filter(Boolean).join(' · '),
        content: envelope ? '' : typeof entry.content === 'string' ? entry.content : '',
        envelope,
        reasoning: explicitReasoning(entry),
        diagnostics: modelDiagnostics(entry),
        tools,
        status: error ? 'failed' : complete ? 'completed' : 'in_progress',
        error,
        tone: error ? 'neg' : null,
      }
    }

    const ok = entry.result?.ok === true
    const error = entry.result?.error
    const resultFields = ok
      ? fields(entry.result?.result)
      : fields(error ? { code: error.code, message: error.message, outcome: error.outcome } : { error: entry.error || '未知錯誤' })
    return {
      key: index,
      kind: 'tool',
      step: `iter ${entry.iteration}`,
      title: `tool · ${entry.tool_name}`,
      meta: ok ? 'ok' : 'error',
      fields: resultFields,
      filtered: ok,
      tone: ok ? null : 'neg',
    }
  })
}
