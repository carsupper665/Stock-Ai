import { afterEach, expect, it } from 'vitest'
import { createApp } from 'vue'

import ActivityList from '../src/shared/ActivityList.vue'
import PositionCard from '../src/shared/PositionCard.vue'

let app
afterEach(() => { app?.unmount(); app = null; document.body.innerHTML = '' })
function mount(component, props) {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp(component, props)
  app.mount(root)
  return root
}

it('position card shows only the fields the position has', () => {
  const root = mount(PositionCard, { position: {
    id: 'p1', symbol: 'BTCUSDT', product: 'futures', side: 'short', leverage: 10, quantity: 0.5, entry_price: 60000,
    mark_price: 59000, margin: 3000, unrealized_pnl: 500, stop_loss: 61000, stop_loss_source: { session_id: 's_1', run_id: 4 },
    opened_at: '2026-10-18T01:00:00Z',
  } })
  const text = root.textContent
  expect(text).toContain('BTCUSDT')
  expect(text).toContain('空')
  expect(text).toContain('10x')
  expect(text).toContain('+16.67%')
  expect(text).toContain('停損61,000')
  expect(text).toContain('Run #4')
  expect(text).not.toContain('停利')
  expect(text).not.toContain('強平')
  expect(text).not.toContain('MMR')
})

it('position card hides margin and return when the position has no margin', () => {
  const root = mount(PositionCard, { position: { id: 'p2', symbol: 'ETHUSDT', product: 'spot', side: 'long', leverage: 1, quantity: 1, entry_price: 2400, mark_price: 2300, margin: 0, unrealized_pnl: -100 } })
  expect(root.textContent).toContain('多')
  expect(root.textContent).not.toContain('保證金')
  expect(root.textContent).not.toContain('回報率')
  expect(root.textContent).not.toContain('開倉')
})

it('order list labels each order in plain words and omits empty fields', () => {
  const root = mount(ActivityList, { kind: 'orders', items: [
    { id: 'ord_1', symbol: 'BTCUSDT', product: 'futures', side: 'buy', type: 'market', quantity: 0.1, leverage: 5, status: 'filled',
      filled_quantity: 0.1, avg_fill_price: 60100, fee: 2.4, realized_pnl: 0, source: { session_id: 's_1', run_id: 3 }, trigger: 'stop_loss', created_at: '2026-10-18T01:00:00Z' },
    { id: 'ord_2', symbol: 'ETHUSDT', side: 'sell', type: 'limit', price: 2500, quantity: 1, leverage: 1, status: 'rejected',
      filled_quantity: 0, fee: 0, realized_pnl: 0, reject_reason: 'insufficient balance', created_at: '2026-10-18T01:00:00Z' },
  ] })
  const [first, second] = root.querySelectorAll('.item')
  expect(first.textContent).toContain('買')
  expect(first.textContent).toContain('市價')
  expect(first.textContent).toContain('5x')
  expect(first.textContent).toContain('成交量 0.1 @ 60,100')
  expect(first.textContent).toContain('已成交')
  expect(first.textContent).toContain('停損觸發')
  expect(first.textContent).toContain('Run #3')
  expect(first.textContent).not.toContain('價格')
  expect(first.textContent).not.toContain('已實現')
  expect(second.textContent).toContain('限價')
  expect(second.textContent).toContain('價格 2,500')
  expect(second.textContent).toContain('已拒絕')
  expect(second.textContent).toContain('insufficient balance')
  expect(second.textContent).not.toContain('1x')
  expect(second.textContent).not.toContain('手續費')
})

it('ledger list names events and shows balance changes only when the balance moved', () => {
  const root = mount(ActivityList, { kind: 'ledger', sessionId: 's_1', items: [
    { seq: 9, event: 'fill', order_id: 'ord_1', quantity: 0.1, price: 60100, fee: 2.4, realized_pnl: 12.5, balance_delta: 10.1, balance_after: 97510.1,
      source: { session_id: 's_other', run_id: 2 }, created_at: '2026-10-18T01:00:00Z' },
    { seq: 8, event: 'stops_updated', position_id: 'pos_1', quantity: 0, price: 0, fee: 0, realized_pnl: 0, balance_delta: 0, balance_after: 97500,
      stop_loss: 59000, take_profit: 0, created_at: '2026-10-18T00:59:00Z' },
  ] })
  const [fill, stops] = root.querySelectorAll('.item')
  expect(fill.textContent).toContain('#9')
  expect(fill.textContent).toContain('成交')
  expect(fill.textContent).toContain('+$10.10')
  expect(fill.textContent).toContain('變動後餘額 97,510.10')
  expect(fill.textContent).toContain('s_other / Run #2')
  expect(stops.textContent).toContain('更新停損停利')
  expect(stops.textContent).toContain('停損 59,000')
  expect(stops.textContent).not.toContain('餘額')
  expect(stops.querySelector('.facts').textContent).not.toContain('停利')
  expect(stops.textContent).toContain('pos_1')
})
