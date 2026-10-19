import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick } from 'vue'
import { createMemoryHistory, createRouter } from 'vue-router'

import Dashboard from '../src/pages/dashboard/Page.vue'
import { alerts, bar, counts, filterAgents, models, nextReview, overview, ranks, sortAgents, toAgent } from '../src/pages/dashboard/agents.js'

const ERR = 'GATEWAY_ERROR: PROVIDER_UNAVAILABLE: CLI invocation failed.'
const position = (side, extra = {}) => ({ id: `pos_${side}`, symbol: 'BTCUSDT', side, leverage: 10, quantity: 0.01, entry_price: 80000, mark_price: 81000, unrealized_pnl: 10, margin: 81, ...extra })
function session(id, { pnl = 0, status = 'running', last = { status: 'completed', summary: 'ok' }, positions = [], tokens = 1000, runs = 2, model = 'm1', events = [] } = {}) {
  return {
    id, name: `agent_${id}`, status, model_name: model, model_level: 'high', total_runs: runs, usage: { total_tokens: tokens },
    current_run: null, last_run: { run_id: runs, ...last }, active_events: events,
    trading: { account: { initial_balance: 1000, equity: 1000 + pnl }, pnl: { net_realized: pnl }, positions, errors: {} },
  }
}

describe('監控面板的資料整理', () => {
  it('後端沒給的值保持未知，不推算', () => {
    const a = toAgent({ id: 's', name: 'n', status: 'running', model_name: 'm', trading: { account: null, pnl: null, positions: null, errors: { positions: { message: 'Backend monitoring is unavailable' } } } })
    expect(a).toMatchObject({ pnl: null, pct: null, equity: null, positions: null, positionsError: 'Backend monitoring is unavailable', tokens: null, last: null, message: '' })
    expect(toAgent(session('x', { pnl: -52.41 })).pct).toBeCloseTo(-5.241)
    expect(toAgent(session('f', { last: { status: 'failed', error: ERR, summary: 'ignored' } })).message).toBe(ERR)
  })

  it('總覽、警示與篩選只看 running；同一個錯誤代碼會提示共同原因', () => {
    const agents = [
      session('a', { pnl: 5, positions: [position('long'), position('short')] }),
      session('b', { pnl: -9, last: { status: 'failed', error: ERR } }),
      session('c', { pnl: -100, last: { status: 'failed', error: ERR }, positions: [position('long')] }),
      session('d', { pnl: 50, status: 'stopped' }),
      session('e', { pnl: 0, status: 'deleted' }),
    ].map(toAgent)
    expect(counts(agents)).toEqual({ running: 3, failed: 2, holding: 2, flat: 1, stopped: 1, deleted: 1 })
    expect(overview(agents).map((k) => [k.value, k.sub])).toEqual([
      ['2 / 3', '最近一次 Run 失敗 · 其中 1 個仍持倉'],
      ['2 / 3', '多 2 · 空 1 筆'],
      ['3.0k', '共 6 個 Run · 每 Run 約 500'],
    ])
    const alert = alerts(agents)
    expect(alert.items.map((i) => [i.name, i.risk])).toEqual([['agent_c', '持倉 1 筆 · 倉位可能無人盯盤'], ['agent_b', '無持倉，無倉位風險']])
    expect(alert.cause).toContain('GATEWAY_ERROR')
    expect(filterAgents(agents, 'failed').map((a) => a.id)).toEqual(['b', 'c'])
    expect(filterAgents(agents, 'deleted').map((a) => a.id)).toEqual(['e'])
    expect(sortAgents(filterAgents(agents, 'running'), 'urgent').map((a) => a.id)).toEqual(['c', 'b', 'a'])
    expect([...ranks(agents)]).toEqual([['d', 1], ['a', 2], ['b', 3], ['c', 4]])
  })

  it('Token：還沒呼叫過模型算 0，呼叫過卻沒回報的另外註明', () => {
    const fresh = { ...session('n', { runs: 0 }), usage: { total_tokens: null, model_calls: 0 } }
    const silent = { ...session('s', { runs: 3 }), usage: { total_tokens: null, model_calls: 3 } }
    expect(toAgent(fresh).tokens).toBe(0)
    expect(toAgent(silent).tokens).toBeNull()
    expect(overview([session('a'), fresh, silent].map(toAgent))[2]).toMatchObject({ value: '1.0k', sub: '共 2 個 Run · 每 Run 約 500 · 1 個沒回報用量' })
  })

  it('下次檢視：計時器排前面，最多兩個', () => {
    const soon = new Date(Date.now() + 60e3).toISOString()
    const later = new Date(Date.now() + 120e3).toISOString()
    expect(nextReview([])).toBe('—')
    const label = nextReview([
      { type: 'position_close', params: { position_id: 'p' } },
      { type: 'timer', params: { at: later } },
      { type: 'timer', params: { at: soon } },
      { type: 'price_cross_above', params: { symbol: 'BTCUSDT', price: 82000 } },
    ])
    expect(label).toMatch(/^\d{2}:\d{2}:\d{2} · \d{2}:\d{2}:\d{2} \+2$/)
    expect(nextReview([{ type: 'price_change_pct', params: { symbol: 'BTCUSDT', direction: 'down', pct: 2 } }])).toBe('BTCUSDT 跌 2%')
  })

  it('依模型比較排除已刪除，平均只算 PnL 已知的', () => {
    const agents = [session('a', { pnl: -10 }), session('b', { pnl: -30 }), session('c', { pnl: 20, model: 'm2' }), session('d', { pnl: 999, status: 'deleted' })].map(toAgent)
    expect(models(agents)).toEqual([
      { name: 'm2', agents: 1, avgPct: 2, avgPnl: 20, total: 20, failed: 0 },
      { name: 'm1', agents: 2, avgPct: -2, avgPnl: -20, total: -40, failed: 0 },
    ])
  })

  it('長條以 0 為基準', () => {
    expect(bar(-50, [-100, 50])).toEqual({ left: '33.33%', width: '33.33%' })
    expect(bar(0.001, [-100, 50]).width).toBe('1.50%')
  })
})

describe('監控面板頁', () => {
  let app
  afterEach(() => { app?.unmount(); app = null; document.body.innerHTML = ''; vi.restoreAllMocks() })

  it('列出 running 的 agent，可切換篩選、展開細節', async () => {
    const sessions = [
      session('a', { pnl: 5, positions: [position('long', { stop_loss: 79000 })] }),
      session('b', { pnl: -9, last: { status: 'failed', error: ERR } }),
      session('d', { pnl: 50, status: 'stopped' }),
    ]
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ sessions }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    const router = createRouter({ history: createMemoryHistory(), routes: [{ path: '/', component: Dashboard }, { path: '/sessions/:id', name: 'session-detail', component: Dashboard }] })
    await router.push('/')
    const root = document.createElement('div'); document.body.append(root)
    app = createApp(Dashboard); app.use(router); app.mount(root)

    await vi.waitFor(() => expect(document.querySelectorAll('.agent')).toHaveLength(2))
    expect(fetch.mock.calls[0][0]).toBe('/agent/api/v1/sessions?page=1&include_deleted=true')
    expect(document.body.textContent).toContain('1 個 Agent 最近一次 Run 失敗')
    expect(document.querySelector('.agent').textContent).toContain('停損 79,000')

    ;[...document.querySelectorAll('.chip')].find((b) => b.textContent.startsWith('已停止')).click()
    await nextTick()
    expect([...document.querySelectorAll('.agent .name')].map((n) => n.textContent)).toEqual(['agent_d'])

    document.querySelector('.agent .head').click()
    await nextTick()
    expect(document.querySelector('.more').textContent).toContain('已停止')
    expect(document.querySelector('.more').textContent).not.toContain('stop')
  })
})
