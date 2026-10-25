// Build first, then: node test/current-run-geometry.mjs <evidence-directory>.
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { networkInterfaces, tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

const browser = process.env.CHROME_PATH ?? '/mnt/c/Program Files/Google/Chrome/Application/chrome.exe'
assert.ok(existsSync(browser), 'Chrome executable is required; no skipped geometry checks')
const windows = browser.endsWith('.exe')
const dist = resolve('dist')
const evidence = resolve(process.argv[2] ?? '../.scratch/user-tag-wake-verbatim/screenshots')
mkdirSync(evidence, { recursive: true })
let state = 'active'
const now = Date.now()
const current = { run_id: 3, status: 'running', trigger: 'event', start_time: new Date(now - 45000).toISOString(), decision_count: 3, model_call_count: 4, tool_call_count: 2, input_tokens: 12000, output_tokens: 800, cached_tokens: 9000, total_tokens: 12800,
  progress: [{ type: 'model_call', iteration: 1, content: '已讀取 USER 的訊息，正在確認市場與風險。', usage: { input_tokens: 12000, cached_tokens: 9000 }, tool_calls: [], ended_at: new Date(now).toISOString() }] }
const session = () => ({ id: 's1', name: 'USER tag 喚醒 Agent', account_id: 'a1', model_name: 'fixture-model', status: ['active', 'idle'].includes(state) ? 'running' : state,
  max_loop: 10, max_tool_call: 20, current_run_id: 3, total_runs: 1, memory_summary: { count: 0 }, active_event_count: 1, pending_trigger_count: 0, skills_enabled: false, conversation_mode: 'verbatim', trading: { account: { user_name: 'fixture-agent', equity: 100000, available: 100000, locked_margin: 0, unrealized_pnl: 0 } } })

const inspection = `<script>
const box = (el) => { if (!el) return null; const r = el.getBoundingClientRect(); return { left:r.left, right:r.right, top:r.top, bottom:r.bottom }; };
function inspect() {
  const band = document.querySelector('.band');
  const runs = new URLSearchParams(location.search).get('tab') === 'runs';
  if (!band || (runs && !document.querySelector('.detail'))) { setTimeout(inspect, 50); return; }
  const left = document.querySelector('.run-left');
  const report = { state: ${JSON.stringify(state)}, width: innerWidth, overflow: document.documentElement.scrollWidth > innerWidth,
    band:box(band), left:box(left), list:box(document.querySelector('.list')), detail:box(document.querySelector('.detail')), tabs:box(document.querySelector('.tabs')),
    bandInLeft:!!band.closest('.run-left'), bandOverflow:band.scrollWidth > band.clientWidth, cards:document.querySelectorAll('.band').length,
    columns:document.querySelector('.run-layout') ? getComputedStyle(document.querySelector('.run-layout')).gridTemplateColumns.split(' ').length : null,
    text:band.textContent };
  const output=document.createElement('pre'); output.id='run-geometry'; output.hidden=true; output.textContent=JSON.stringify(report); document.body.append(output);
}
setTimeout(inspect, 50);
</script>`

function json(response, body, status = 200) { response.writeHead(status, { 'Content-Type': 'application/json' }); response.end(JSON.stringify(body)) }
const server = createServer((request, response) => {
  const path = new URL(request.url, 'http://localhost').pathname
  if (path === '/oauth2/me') return json(response, { user_name: 'USER', expires_at: new Date(now + 3600000).toISOString() })
  if (path === '/v1/me') return json(response, { kind: 'user', user_name: 'USER' })
  if (path.endsWith('/runs/current')) return state === 'active' ? json(response, current) : json(response, { error: 'CURRENT_RUN_NOT_FOUND' }, 404)
  if (path.endsWith('/stream')) { response.writeHead(200, { 'Content-Type': 'text/event-stream' }); return response.end('retry: 3600000\n: ping\n\n') }
  if (path === '/agent/api/v1/sessions/s1') return json(response, session())
  if (path.endsWith('/runs')) return json(response, { runs: [current], page: 1 })
  if (path.endsWith('/conversation')) return json(response, { messages: [], estimated_tokens: 0, context_window: 128000 })
  if (path.endsWith('/memories')) return json(response, { memories: [], capacity: 60 })
  if (path.endsWith('/usage')) return json(response, { by_model: [], unknown_attempts: {} })
  if (path.endsWith('/tools/stats')) return json(response, { tools: [] })
  if (path.endsWith('/positions')) return json(response, { positions: [] })
  if (path.endsWith('/ledger')) return json(response, { entries: [] })
  if (path.endsWith('health') || path.endsWith('healthz')) return json(response, { status: 'ok', version: 'fixture' })
  const file = path.startsWith('/assets/') ? join(dist, 'assets', basename(path)) : join(dist, 'index.html')
  try {
    let body = readFileSync(file)
    // Capture the current fixture state at the time this page is served.
    if (file.endsWith('.html')) body = Buffer.from(body.toString().replace('<head>', '<head>' + inspection.replace('"active"', JSON.stringify(state))))
    response.writeHead(200, { 'Content-Type': file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' }); response.end(body)
  } catch { response.writeHead(404); response.end() }
})

async function inspect(origin, width, tab, screenshot) {
  const profile = mkdtempSync(join(windows ? '/mnt/c/Windows/Temp' : tmpdir(), 'run-browser-'))
  const native = (path) => windows ? `${path[5].toUpperCase()}:\\${path.slice(7).replaceAll('/', '\\')}` : path
  try {
    const output = await new Promise((resolveRun, reject) => {
      const args = ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${native(profile)}`, `--window-size=${width},1000`, '--virtual-time-budget=8000', '--dump-dom']
      if (screenshot) args.push(`--screenshot=${native(join(profile, 'shot.png'))}`)
      args.push(`${origin}/sessions/s1?tab=${tab}`)
      const child = spawn(browser, args)
      let output = '', errors = ''
      const timeout = setTimeout(() => { child.kill(); reject(new Error('Chrome timed out')) }, 45000)
      child.stdout.on('data', (chunk) => { output += chunk })
      child.stderr.on('data', (chunk) => { errors += chunk })
      child.on('error', (error) => { clearTimeout(timeout); reject(error) })
      child.on('close', (code) => { clearTimeout(timeout); code ? reject(new Error(`Chrome ${code}: ${errors.slice(-1000)}`)) : resolveRun(output) })
    })
    const match = output.match(/<pre id="run-geometry" hidden="">([^<]+)<\/pre>/)
    assert.ok(match, `No geometry report: ${output.slice(-1000)}`)
    if (screenshot) copyFileSync(join(profile, 'shot.png'), join(evidence, screenshot))
    return JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&'))
  } finally { rmSync(profile, { recursive: true, force: true }) }
}

await new Promise((ready) => server.listen(0, '0.0.0.0', ready))
try {
  const host = windows ? networkInterfaces().eth0?.find((item) => item.family === 'IPv4')?.address : '127.0.0.1'
  assert.ok(host, 'A reachable WSL IPv4 address is required for Windows Chrome')
  const origin = `http://${host}:${server.address().port}`
  const reports = []
  for (const status of ['active', 'idle', 'stopped', 'deleted']) {
    state = status
    for (const width of [1400, 500]) {
      const report = await inspect(origin, width, 'runs', status === 'active' && width === 1400 ? '07-runs-desktop.png' : null)
      const detail = JSON.stringify(report)
      assert.equal(report.overflow, false, detail)
      assert.equal(report.cards, 1, detail)
      assert.equal(report.bandOverflow, false, detail)
      assert.equal(report.bandInLeft, true, detail)
      assert.ok(report.band.left >= report.left.left && report.band.right <= report.left.right + 1, detail)
      assert.ok(report.band.bottom <= report.list.top + 1, detail)
      if (width === 1400) { assert.equal(report.columns, 2, detail); assert.ok(Math.abs(report.band.top - report.detail.top) < 2, detail) }
      else { assert.equal(report.columns, 1, detail); assert.ok(report.list.bottom <= report.detail.top + 1, detail) }
      reports.push(report)
    }
  }
  state = 'active'
  for (const tab of ['overview', 'conversation', 'memory', 'monitor']) {
    const report = await inspect(origin, 1400, tab, tab === 'overview' ? '07-overview-desktop.png' : null)
    assert.equal(report.overflow, false, JSON.stringify(report))
    assert.equal(report.bandInLeft, false)
    assert.ok(report.band.bottom <= report.tabs.top)
    reports.push(report)
  }
  writeFileSync(join(evidence, '07-geometry.json'), JSON.stringify(reports, null, 2) + '\n')
  console.log(`PASS: 12 browser layouts; screenshots and geometry in ${evidence}`)
} finally { server.close() }
