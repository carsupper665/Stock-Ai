// 只做顯示格式，不做金額運算（REQUIREMENTS X-04）

export const num = (v, digits) => (v == null ? '—' : Number(v).toLocaleString('en-US', digits == null ? { maximumFractionDigits: 8 } : { minimumFractionDigits: digits, maximumFractionDigits: digits }))

// 正負金額：正號 +、負號用 U+2212，零不帶符號
export function money(v) {
  if (v == null) return '—'
  const n = Number(v)
  const sign = n > 0 ? '+' : n < 0 ? '−' : ''
  return `${sign}$${num(Math.abs(n), 2)}`
}
export const pnlTone = (v) => (v > 0 ? 'pos' : v < 0 ? 'neg' : 'dim')

export const abbr = (v) => (v == null ? '—' : v >= 1e6 ? `${(v / 1e6).toFixed(2)}M` : v >= 1e3 ? `${(v / 1e3).toFixed(1)}k` : String(v))

// 時間：後端一律 UTC RFC3339，顯示轉瀏覽器本地時間
export const fmtTime = (iso) => (iso ? new Date(iso).toLocaleString('zh-TW', { hour12: false }) : '—')
export const fmtClock = (iso) => (iso ? new Date(iso).toLocaleTimeString('zh-TW', { hour12: false }) : '—')

export function relTime(iso, now = Date.now()) {
  if (!iso) return '—'
  const s = Math.max(0, Math.round((now - new Date(iso).getTime()) / 1000))
  if (s < 60) return `${s} 秒前`
  if (s < 3600) return `${Math.floor(s / 60)} 分鐘前`
  if (s < 86400) return `${Math.floor(s / 3600)} 小時前`
  return `${Math.floor(s / 86400)} 天前`
}

export const mmss = (sec) => `${String(Math.floor(sec / 60)).padStart(2, '0')}:${String(Math.floor(sec % 60)).padStart(2, '0')}`

export function duration(startIso, endIso) {
  if (!startIso || !endIso) return '—'
  const ms = new Date(endIso) - new Date(startIso)
  return ms < 60000 ? `${(ms / 1000).toFixed(1)}s` : mmss(ms / 1000)
}

// 狀態 → 語意色（設計交接稿「狀態色映射」；interrupted 絕不能長得像 completed）
const tones = {
  running: 'acc', open: 'acc', enabled: 'pos',
  completed: 'pos', active: 'pos', filled: 'pos', long: 'pos', buy: 'pos',
  interrupted: 'warn', activating: 'warn',
  failed: 'neg', rejected: 'neg', short: 'neg', sell: 'neg',
  stopped: 'mute', deleted: 'mute', disabled: 'mute', inactive: 'mute', canceled: 'mute',
}
export const tone = (status) => tones[status] ?? 'dim'
