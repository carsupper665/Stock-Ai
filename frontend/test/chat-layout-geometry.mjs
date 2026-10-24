// 群聊頁版面：用真的 Chrome 量寬螢幕兩欄、900px 以下只顯示聊天、側欄抽屜與成員選單，並確認沒有水平捲軸。
// 先 npm run build，再 node test/chat-layout-geometry.mjs [--screenshots <dir>]。
import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync, copyFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { networkInterfaces, tmpdir } from 'node:os'
import { basename, join, resolve } from 'node:path'

const browser = process.env.CHROME_PATH ?? '/mnt/c/Program Files/Google/Chrome/Application/chrome.exe'
assert.ok(existsSync(browser), 'Set CHROME_PATH to a Chrome/Chromium executable')
const dist = resolve('dist')
assert.ok(existsSync(join(dist, 'index.html')), 'Build the frontend first')
const reportPath = resolve('../docs/api/chat-layout-geometry.json')
const shotDir = process.argv.includes('--screenshots') ? resolve(process.argv[process.argv.indexOf('--screenshots') + 1]) : null
const windows = browser.endsWith('.exe')

const long = 'LongAccountName'.repeat(4)
const accounts = [
  { id: 'a1', user_name: 'btc-runner', status: 'active' }, { id: 'a2', user_name: 'eth-waiter', status: 'active' },
  { id: 'a3', user_name: 'sol-stopped', status: 'active' }, { id: 'a4', user_name: long, status: 'disabled' },
  { id: 'a5', user_name: 'unbound-1', status: 'active' }, { id: 'a6', user_name: 'gone', status: 'active' },
].map((account) => ({ chat_read: true, chat_post: true, chat_muted_until: null, chat_mute_reason: null, chat_rate_minutes: 1, chat_rate_count: 3, ...account }))
const sessions = [
  { id: 's1', account_id: 'a1', name: 'BTC 趨勢 Agent', status: 'running', current_run: { run_id: 4 } },
  { id: 's2', account_id: 'a2', name: 'ETH 網格 Agent', status: 'running', current_run: null },
  { id: 's3', account_id: 'a3', name: 'SOL 觀察 Agent', status: 'stopped', current_run: null },
  { id: 's4', account_id: 'a4', name: '名稱很長的 Session '.repeat(4), status: 'stopped', current_run: null },
  { id: 's6', account_id: 'a6', name: 'Gone', status: 'deleted', current_run: null, deleted_at: '2026-10-01T00:00:00Z' },
]
const now = Date.now()
const messages = [
  ['btc-runner', 'BTC 在 6 萬附近反覆測試支撐，我把停損移到 59,200。', []],
  ['you', '收到，記得在群聊同步調整後的部位理由。', ['btc-runner']],
  ['eth-waiter', '@USER 網格參數要不要放寬？最近波動變大，這是一則比較長的訊息，用來確認氣泡換行與最大寬度不會撐破版面。'.repeat(2), ['USER']],
  ['gone', '這則來自 Session 已刪除的帳戶。', []],
  ['[deleted]', '這則來自已刪除的帳戶。', []],
  [long, 'x'.repeat(200), []],
  ['sol-stopped', '我先停機觀察。', ['USER']],
].map(([user_name, content, tags], index) => ({ id: `m${index}`, user_name, content, tags, created_at: new Date(now - (10 - index) * 60000).toISOString() }))

const inspection = `<script>
const forced = new URLSearchParams(location.search).get('theme'); if (forced) localStorage.setItem('theme', forced);
const box = (el) => { const r = el.getBoundingClientRect(); return { left: r.left, top: r.top, right: r.right, bottom: r.bottom }; };
const visible = (el) => !!el && getComputedStyle(el).display !== 'none' && el.getBoundingClientRect().width > 0;
const overflow = () => document.documentElement.scrollWidth > innerWidth;
const wait = () => new Promise((resolve) => setTimeout(resolve, 150));
async function inspect() {
  const chat = document.querySelector('.chat'), msgs = document.querySelector('.msgs');
  if (!chat || !document.querySelector('.msg') || !document.querySelector('.member')) { setTimeout(inspect, 100); return; }
  // 只有版面變（例如字型載入）時是靠 ResizeObserver 捲回底部，而 headless 的虛擬時間有時不出繪製幀、不會回呼；
  // 所以這裡用頁面自己判斷「在底部」的 40px 門檻，最多等 1.5 秒
  const bottomGap = () => msgs.scrollHeight - msgs.scrollTop - msgs.clientHeight;
  for (let i = 0; i < 10 && bottomGap() >= 2; i++) await wait();
  const report = { width: innerWidth, horizontalOverflow: overflow(), pageFitsScreen: document.documentElement.scrollHeight <= innerHeight + 1,
    msgsScroll: msgs.scrollHeight > msgs.clientHeight, msgsAtBottom: bottomGap() < 40, scroll: [msgs.scrollTop, msgs.scrollHeight, msgs.clientHeight],
    chat: box(chat), memberPane: visible(document.querySelector('.member-pane')) ? box(document.querySelector('.member-pane')) : null,
    shellAside: visible(document.querySelector('#shell > aside')), shellHeader: visible(document.querySelector('#shell header')),
    backButton: visible(document.querySelector('[aria-label="打開側欄"]')), membersButton: visible(document.querySelector('[aria-label="成員"]')),
    overflowing: [...document.querySelectorAll('body *')].filter((el) => el.getBoundingClientRect().right > innerWidth + 1).slice(0, 8).map((el) => el.tagName + '.' + el.className) };
  if (report.backButton) {
    document.querySelector('[aria-label="打開側欄"]').click(); await wait();
    const aside = document.querySelector('#shell > aside');
    report.drawer = { visible: visible(aside), position: getComputedStyle(aside).position, box: box(aside), overflow: overflow() };
    document.querySelector('.drawer-backdrop').click(); await wait();
    report.drawerClosed = !visible(aside);
    document.querySelector('[aria-label="成員"]').click(); await wait();
    const sheet = document.querySelector('.member-sheet');
    report.sheet = { visible: visible(sheet), members: sheet.querySelectorAll('.member').length, overflow: overflow() };
    sheet.querySelector('.member').click(); await wait();
  } else {
    document.querySelector('.member-pane .member').click(); await wait();
  }
  const dialog = document.querySelector('.member-dialog');
  report.dialog = dialog ? { open: dialog.open, box: box(dialog), overflow: overflow() } : null;
  if (!location.search.includes('shot')) document.querySelector('[aria-label="關閉"]')?.click();
  const output = document.createElement('pre'); output.id = 'chat-geometry'; output.hidden = true; output.textContent = JSON.stringify(report); document.body.append(output);
}
setTimeout(inspect, 800);
</script>`

function json(response, body) {
  response.writeHead(200, { 'Content-Type': 'application/json' })
  response.end(JSON.stringify(body))
}

const server = createServer((request, response) => {
  const url = new URL(request.url, 'http://localhost')
  const path = url.pathname
  if (path === '/oauth2/me') return json(response, { user_name: 'Bless', expires_at: new Date(Date.now() + 3600000).toISOString() })
  if (path === '/v1/me') return json(response, { kind: 'user', user_name: 'Bless' })
  if (path === '/v1/accounts') return json(response, { accounts })
  if (path === '/agent/api/v1/sessions') return json(response, { sessions })
  if (path === '/v1/messages/stream') {
    // 長連線會讓 Chrome 的虛擬時間停不下來：送完重連間隔就結束，瀏覽器一小時後才重連
    response.writeHead(200, { 'Content-Type': 'text/event-stream' })
    return response.end('retry: 3600000\n: ping\n\n')
  }
  if (path === '/v1/messages') return json(response, { messages: [...messages].reverse(), page: 1, has_more: true })
  if (path.endsWith('health') || path.endsWith('healthz')) return json(response, { status: 'ok', version: 'test' })
  const file = path.startsWith('/assets/') ? join(dist, 'assets', basename(path)) : join(dist, 'index.html')
  try {
    let body = readFileSync(file)
    if (file.endsWith('.html')) body = Buffer.from(body.toString().replace('<head>', '<head>' + inspection))
    response.writeHead(200, { 'Content-Type': file.endsWith('.js') ? 'text/javascript' : file.endsWith('.css') ? 'text/css' : 'text/html' })
    response.end(body)
  } catch { response.writeHead(404); response.end() }
})

function chrome(args) {
  // Chrome 的 SQLite profile 需要 Windows 原生的檔案鎖。
  const profile = mkdtempSync(join(windows ? '/mnt/c/Windows/Temp' : tmpdir(), 'chat-browser-'))
  const native = (path) => windows ? `${path[5].toUpperCase()}:\\${path.slice(7).replaceAll('/', '\\')}` : path
  const run = new Promise((resolveRun, reject) => {
    const child = spawn(browser, ['--headless', '--disable-gpu', '--no-first-run', '--no-default-browser-check', `--user-data-dir=${native(profile)}`, ...args(profile, native)])
    let output = '', errors = ''
    const timeout = setTimeout(() => { child.kill(); reject(new Error('Chrome timed out')) }, 45000)
    child.stdout.on('data', (data) => { output += data })
    child.stderr.on('data', (data) => { errors += data })
    child.on('error', (error) => { clearTimeout(timeout); reject(error) })
    child.on('close', (code) => { clearTimeout(timeout); code ? reject(new Error(`Chrome failed (${code}): ${errors.slice(-1500)}`)) : resolveRun({ output, profile }) })
  })
  return run.finally(() => setTimeout(() => rmSync(profile, { recursive: true, force: true }), 500))
}

async function inspect(origin, width) {
  const { output } = await chrome(() => [`--window-size=${width},900`, '--virtual-time-budget=20000', '--dump-dom', origin + '/messages'])
  const match = output.match(/<pre id="chat-geometry" hidden="">([^<]+)<\/pre>/)
  if (!match) throw new Error(`No report at ${width}px: ${output.slice(-800)}`)
  return JSON.parse(match[1].replaceAll('&quot;', '"').replaceAll('&amp;', '&'))
}

async function screenshot(origin, width, height, theme, name) {
  await chrome((profile, native) => [`--window-size=${width},${height}`, '--virtual-time-budget=8000', '--hide-scrollbars', `--screenshot=${native(join(profile, 'shot.png'))}`, `${origin}/messages?theme=${theme}${name.includes('dialog') ? '&shot' : ''}`])
    .then(({ profile }) => copyFileSync(join(profile, 'shot.png'), join(shotDir, `${name}.png`)))
}

await new Promise((resolveReady) => server.listen(0, '0.0.0.0', resolveReady))
try {
  const host = windows ? networkInterfaces().eth0?.find((item) => item.family === 'IPv4')?.address : '127.0.0.1'
  assert.ok(host, 'Windows Chrome needs a reachable WSL eth0 address')
  const origin = `http://${host}:${server.address().port}`
  const reports = []
  for (const width of [1400, 1000, 901, 900, 600, 375]) {
    const report = await inspect(origin, width)
    const detail = JSON.stringify(report)
    assert.equal(report.horizontalOverflow, false, detail)
    assert.equal(report.pageFitsScreen, true, detail)
    assert.equal(report.msgsScroll, true, detail)
    assert.equal(report.msgsAtBottom, true, detail)
    assert.equal(report.dialog?.open, true, detail)
    assert.equal(report.dialog.overflow, false, detail)
    assert.ok(report.dialog.box.left >= 0 && report.dialog.box.right <= report.width, detail)
    if (report.width > 900) {
      assert.ok(report.memberPane && report.memberPane.right <= report.chat.left + 1, detail)
      assert.equal(report.shellAside, true, detail)
      assert.equal(report.backButton, false, detail)
    } else {
      assert.equal(report.memberPane, null, detail)
      assert.equal(report.shellAside, false, detail)
      assert.equal(report.shellHeader, false, detail)
      assert.ok(report.chat.left === 0 && Math.abs(report.chat.right - report.width) < 1, detail)
      assert.equal(report.drawer.visible, true, detail)
      assert.equal(report.drawer.position, 'fixed', detail)
      assert.equal(report.drawer.overflow, false, detail)
      assert.equal(report.drawerClosed, true, detail)
      assert.equal(report.sheet.visible, true, detail)
      assert.equal(report.sheet.members, 5, detail)
    }
    reports.push(report)
  }
  writeFileSync(reportPath, JSON.stringify(reports, null, 2) + '\n')
  console.log('PASS TestChatLayoutGeometry: wide two-column, narrow chat-only with drawer and member sheet, no horizontal scroll')
  if (shotDir) {
    for (const theme of ['light', 'dark']) {
      await screenshot(origin, 1400, 900, theme, `wide-${theme}`)
      await screenshot(origin, 500, 900, theme, `narrow-${theme}`)
      await screenshot(origin, 1400, 900, theme, `wide-dialog-${theme}`)
    }
    console.log(`screenshots in ${shotDir}`)
  }
} finally { server.close() }
