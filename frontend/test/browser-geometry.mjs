import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFile, rmSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

const chrome = [
  process.env.CHROME_PATH,
  'C:\\Program Files\\Google\\Chrome\\Application\\chrome.exe',
  'C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe',
].find((path) => path && existsSync(path))

if (!chrome) {
  console.log('browser geometry skipped: Chrome/Edge not found')
  process.exit(0)
}

const dist = resolve('dist')
assert.ok(existsSync(join(dist, 'index.html')), 'run npm run build before browser geometry')

const session = {
  id: 'session_1', name: 'Geometry', status: 'stopped', current_run_id: 100, total_runs: 100,
  max_loop: 200, max_tool_call: 300, memory_summary: { active_count: 0 }, active_event_count: 0,
  pending_trigger_count: 0, trading: { account: {} },
}
const long = 'long content '.repeat(80)
const progress = Array.from({ length: 120 }, (_, index) => index % 2
  ? { type: 'tool_call', iteration: index, tool_name: `tool_${index}`, result: { ok: true, result: { value: long } } }
  : { type: 'model_call', iteration: index, reasoning: long, content: long, tool_calls: [] })
const runs = Array.from({ length: 100 }, (_, index) => ({
  run_id: index + 1, status: 'completed', trigger: 'event', start_time: '2026-09-17T00:00:00Z',
  end_time: '2026-09-17T00:00:01Z', tool_call_count: 60, decision_count: 60, model_call_count: 60,
  progress: index === 99 ? progress : [], output: index === 99 ? long : '',
}))
const positions = Array.from({ length: 30 }, (_, index) => ({ id: `p${index}`, symbol: `BTC${index}`, side: 'long', quantity: 1, entry_price: 1, mark_price: 2, unrealized_pnl: 1 }))
const tools = Array.from({ length: 100 }, (_, index) => ({ tool_name: `tool_${index}`, call_count: index, success_count: index, error_count: 0, in_flight_count: 0, avg_latency_ms: 10, max_latency_ms: 20, last_latency_ms: 15 }))
const entries = Array.from({ length: 100 }, (_, index) => ({ seq: 100 - index, event: 'fill', balance_delta: 1, created_at: '2026-09-17T00:00:00Z', source: null }))

const geometryScript = `<script>
sessionStorage.setItem('token','browser-test'); sessionStorage.setItem('name','USER');
setTimeout(() => {
  const report = { query: location.search, viewport: [innerWidth, innerHeight] };
  if (location.search.includes('monitor')) {
    const names = ['positions','ledger','usage','tools'];
    const boxes = Object.fromEntries(names.map(name => [name, document.querySelector('.' + name).getBoundingClientRect()]));
    const toolScroll = document.querySelector('.tools-scroll');
    report.positionsBeforeLedger = boxes.positions.top < boxes.ledger.top;
    report.positionsNotBelowToolStats = boxes.positions.top < boxes.tools.bottom;
    report.desktopColumnTops = [boxes.positions.top, boxes.usage.top];
    report.readingOrder = names.sort((a, b) => boxes[a].top - boxes[b].top);
    report.toolOverflow = [toolScroll.clientHeight, toolScroll.scrollHeight, getComputedStyle(toolScroll).overflowY];
  } else {
    const list = document.querySelector('.list-scroll'), timeline = document.querySelector('.timeline');
    report.list = [list.clientHeight, list.scrollHeight, getComputedStyle(list).overflowY];
    report.timeline = [timeline.clientHeight, timeline.scrollHeight, getComputedStyle(timeline).overflowY];
    report.actions = getComputedStyle(document.querySelector('.run-actions')).position;
    report.details = [document.querySelectorAll('details').length, document.querySelectorAll('details[open]').length];
  }
  const output = document.createElement('pre'); output.id = 'geometry-report'; output.textContent = JSON.stringify(report); document.body.append(output);
}, 2500);
</script>`

function json(response, body) {
  response.writeHead(200, { 'Content-Type': 'application/json' })
  response.end(JSON.stringify(body))
}

const server = createServer((request, response) => {
  const url = new URL(request.url, 'http://127.0.0.1')
  if (['/v1/health', '/agent/health', '/llm/healthz'].includes(url.pathname)) return json(response, { status: 'ok' })
  if (url.pathname === '/agent/api/v1/sessions/session_1') return json(response, session)
  if (url.pathname.endsWith('/usage')) return json(response, { by_model: [], unknown_attempts: {} })
  if (url.pathname.endsWith('/tools/stats')) return json(response, { tools })
  if (url.pathname.endsWith('/positions')) return json(response, { positions })
  if (url.pathname.endsWith('/ledger')) return json(response, { entries })
  if (url.pathname.endsWith('/runs')) return json(response, { runs })

  const file = url.pathname.startsWith('/assets/') ? join(dist, basename(url.pathname)) : join(dist, 'index.html')
  const actualFile = url.pathname.startsWith('/assets/') ? join(dist, 'assets', basename(url.pathname)) : file
  readFile(actualFile, (error, body) => {
    if (error) { response.writeHead(404); response.end(); return }
    if (actualFile.endsWith('index.html')) body = Buffer.from(body.toString().replace('<head>', `<head>${geometryScript}`))
    const type = actualFile.endsWith('.js') ? 'text/javascript' : actualFile.endsWith('.css') ? 'text/css' : 'text/html'
    response.writeHead(200, { 'Content-Type': type })
    response.end(body)
  })
})

function inspect(url, width) {
  const profile = mkdtempSync(join(tmpdir(), 'stock-ai-browser-'))
  return new Promise((resolveInspection, reject) => {
    const child = spawn(chrome, [
      '--headless', '--disable-gpu', '--no-first-run', `--user-data-dir=${profile}`,
      `--window-size=${width},900`, '--virtual-time-budget=5000', '--dump-dom', url,
    ])
    let output = '', errors = ''
    child.stdout.on('data', (chunk) => { output += chunk })
    child.stderr.on('data', (chunk) => { errors += chunk })
    child.on('error', reject)
    child.on('close', (code) => {
      rmSync(profile, { recursive: true, force: true })
      const match = output.match(/<pre id="geometry-report">([^<]+)<\/pre>/)
      if (code || !match) return reject(new Error(`browser geometry failed (${code}): ${errors.slice(-1000)}`))
      resolveInspection(JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&')))
    })
  })
}

await new Promise((resolveListening) => server.listen(0, '127.0.0.1', resolveListening))
try {
  const origin = `http://127.0.0.1:${server.address().port}`
  const desktop = await inspect(`${origin}/sessions/session_1?tab=monitor`, 1400)
  assert.equal(desktop.positionsBeforeLedger, true)
  assert.equal(desktop.positionsNotBelowToolStats, true)
  assert.ok(Math.abs(desktop.desktopColumnTops[0] - desktop.desktopColumnTops[1]) < 2)
  assert.ok(desktop.toolOverflow[0] <= 430 && desktop.toolOverflow[1] > desktop.toolOverflow[0])
  assert.equal(desktop.toolOverflow[2], 'auto')

  const mobile = await inspect(`${origin}/sessions/session_1?tab=monitor`, 800)
  assert.deepEqual(mobile.readingOrder, ['positions', 'ledger', 'usage', 'tools'])

  const runPage = await inspect(`${origin}/sessions/session_1?tab=runs`, 1400)
  assert.ok(runPage.list[0] <= 720 && runPage.list[1] > runPage.list[0])
  assert.ok(runPage.timeline[0] <= 680 && runPage.timeline[1] > runPage.timeline[0])
  assert.equal(runPage.list[2], 'auto')
  assert.equal(runPage.timeline[2], 'auto')
  assert.equal(runPage.actions, 'sticky')
  assert.deepEqual(runPage.details, [181, 0])
  console.log(JSON.stringify({ desktop, mobile, runPage }))
} finally {
  server.close()
}
