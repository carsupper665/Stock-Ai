import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

const browser = process.env.CHROME_PATH ?? '/mnt/c/Program Files/Google/Chrome/Application/chrome.exe'
assert.ok(existsSync(browser), 'Set CHROME_PATH to a Chrome/Chromium executable')
const dist = resolve('dist')
assert.ok(existsSync(join(dist, 'index.html')), 'Build the frontend first')
const reportPath = resolve('../docs/api/chat-management-geometry.json')
const account = { id: 'a', user_name: 'LongName'.repeat(8), status: 'disabled', chat_read: true, chat_post: false,
  chat_muted_until: new Date(Date.now() + 600000).toISOString(), chat_mute_reason: '原因'.repeat(100), chat_rate_minutes: 1440, chat_rate_count: 100 }

const inspection = `<script>
async function inspect() {
  const panel = document.querySelector('.members'), board = document.querySelector('.board');
  if (!panel || !board) { setTimeout(inspect,100); return; }
  const p = panel.getBoundingClientRect(), b = board.getBoundingClientRect();
  const report = { width: innerWidth, panel: {left:p.left,top:p.top,right:p.right,bottom:p.bottom}, board: {left:b.left,top:b.top,right:b.right,bottom:b.bottom}, horizontalOverflow: document.documentElement.scrollWidth > innerWidth,
    overflowing: [...document.querySelectorAll('body *')].filter(el=>el.getBoundingClientRect().right>innerWidth).slice(0,10).map(el=>el.tagName+'.'+el.className) };
  panel.querySelector('summary').click();
  await new Promise(resolve => requestAnimationFrame(resolve));
  report.collapsed = !panel.open;
  report.collapsedHeight = panel.getBoundingClientRect().height;
  panel.querySelector('summary').click();
  report.reopened = panel.open;
  const output = document.createElement('pre'); output.id='chat-geometry'; output.textContent=JSON.stringify(report); document.body.append(output);
}
setTimeout(inspect,1000);
</script>`

function json(response, body) {
  response.writeHead(200, { 'Content-Type': 'application/json' })
  response.end(JSON.stringify(body))
}

const server = createServer((request, response) => {
  const path = new URL(request.url, 'http://localhost').pathname
  if (path === '/oauth2/me') return json(response, { user_name: 'USER', expires_at: new Date(Date.now() + 3600000).toISOString() })
  if (path === '/v1/accounts') return json(response, { accounts: [account] })
  if (path === '/v1/messages') return json(response, { messages: [], page: 1, has_more: false })
  if (path.endsWith('health') || path.endsWith('healthz')) return json(response, { status: 'ok', version: 'test' })
  const file = path.startsWith('/assets/') ? join(dist, 'assets', basename(path)) : join(dist, 'index.html')
  try {
    let body = readFileSync(file)
    if (file.endsWith('.html')) body = Buffer.from(body.toString().replace('<head>', '<head>' + inspection))
    response.writeHead(200, { 'Content-Type': file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' })
    response.end(body)
  } catch { response.writeHead(404); response.end() }
})

async function inspect(origin, width) {
  const profile = mkdtempSync(join(tmpdir(), 'chat-browser-'))
  const windows = browser.endsWith('.exe')
  const browserProfile = windows ? `\\\\wsl.localhost\\${process.env.WSL_DISTRO_NAME}\\${profile.slice(1).replaceAll('/', '\\')}` : profile
  try {
    return await new Promise((resolveReport, reject) => {
      const child = spawn(browser, ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check',
        `--user-data-dir=${browserProfile}`, `--window-size=${width},1000`, '--virtual-time-budget=10000', '--dump-dom', origin + '/messages'])
      let output = '', errors = ''
      const timeout = setTimeout(() => { child.kill(); reject(new Error('Chrome inspection timed out')) }, 45000)
      child.stdout.on('data', (data) => { output += data })
      child.stderr.on('data', (data) => { errors += data })
      child.on('error', (error) => { clearTimeout(timeout); reject(error) })
      child.on('close', (code) => {
        clearTimeout(timeout)
        const match = output.match(/<pre id="chat-geometry">([^<]+)<\/pre>/)
        if (code || !match) return reject(new Error(`Chrome failed (${code}): ${errors.slice(-1500)} DOM: ${output.slice(-1000)}`))
        resolveReport(JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&')))
      })
    })
  } finally { rmSync(profile, { recursive: true, force: true }) }
}

await new Promise((resolveReady) => server.listen(0, '0.0.0.0', resolveReady))
try {
  // Windows Chrome reaches WSL via localhost forwarding, like the Windows test package.
  const origin = `http://127.0.0.1:${server.address().port}`
  const reports = []
  for (const width of [1400, 900, 899, 600, 500]) {
    const report = await inspect(origin, width)
    assert.equal(report.horizontalOverflow, false, JSON.stringify(report))
    assert.equal(report.collapsed, true)
    assert.equal(report.reopened, true)
    assert.ok(report.collapsedHeight < 100, 'Collapsed panel should show only its summary')
    if (report.width <= 900) assert.ok(report.panel.bottom <= report.board.top, 'Mobile panel must precede messages')
    else assert.ok(report.panel.left >= report.board.right, 'Desktop panel must be beside messages')
    reports.push(report)
  }
  writeFileSync(reportPath, JSON.stringify(reports, null, 2) + '\n')
  console.log('PASS TestChatManagementResponsiveGeometry: desktop/mobile placement, collapse and no horizontal overflow')
  console.log(JSON.stringify(reports))
} finally { server.close() }
