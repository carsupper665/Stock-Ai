import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick, reactive } from 'vue'

import Runs from '../src/pages/session-detail/Runs.vue'
import { conversation } from '../src/shared/run.js'

class FakeEventSource {
  static opened = []
  constructor(url) {
    this.url = url
    this.listeners = {}
    this.closed = false
    FakeEventSource.opened.push(this)
  }
  addEventListener(name, listener) { (this.listeners[name] ??= []).push(listener) }
  close() { this.closed = true }
  emit(name, data) { for (const listener of this.listeners[name] ?? []) listener({ data: JSON.stringify(data) }) }
}

let app
beforeEach(() => {
  FakeEventSource.opened = []
  vi.stubGlobal('EventSource', FakeEventSource)
})
afterEach(() => {
  app?.unmount()
  app = null
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const runningRun = (progress) => ({ run_id: 4, status: 'running', trigger: 'event', start_time: '2026-10-21T00:00:00Z', progress })

function mountRuns(state, onEnded = () => {}) {
  const root = document.createElement('div')
  document.body.append(root)
  app = createApp({ render: () => h(Runs, { session: { id: 'sess/1' }, runs: state.runs, page: 1, onEnded }) })
  app.mount(root)
}

it('uses the live text for the model call that is still streaming and the stored text once it ends', () => {
  const live = { calls: { 1: { content: 'Look', reasoning: 'Check the\nprice' } } }
  const streaming = conversation(runningRun([
    { type: 'model_resolution' },
    { type: 'model_call', iteration: 1, started_at: 'x' },
  ]), live)
  expect(streaming.find((item) => item.kind === 'model')).toMatchObject({ content: 'Look', reasoning: 'Check the\nprice', thinking: true })

  const ended = conversation(runningRun([
    { type: 'model_resolution' },
    { type: 'model_call', iteration: 1, started_at: 'x', ended_at: 'y', content: 'Final text', tool_calls: [] },
  ]), live)
  expect(ended.find((item) => item.kind === 'model')).toMatchObject({ content: 'Final text', reasoning: '', thinking: false })
})

it('streams the open model call into the Run view and asks for a refresh when the Run ends', async () => {
  const ended = vi.fn()
  const state = reactive({ runs: [runningRun([{ type: 'model_call', iteration: 1, started_at: 'x' }])] })
  mountRuns(state, ended)
  await nextTick()
  expect(FakeEventSource.opened).toHaveLength(1)
  const source = FakeEventSource.opened[0]
  expect(source.url).toBe('/agent/api/v1/sessions/sess%2F1/runs/4/stream')

  source.emit('call', { index: 0 })
  source.emit('delta', { reasoning: 'Check BTC\nthen ETH' })
  source.emit('delta', { content: 'Look' })
  source.emit('delta', { content: 'ing' })
  await nextTick()
  expect(document.querySelector('.message > .text').textContent).toBe('Looking')
  expect(document.querySelector('details.reasoning .preview').textContent).toBe('then ETH')
  expect(document.querySelector('.thinking')).not.toBeNull()

  source.emit('end', {})
  await nextTick()
  expect(source.closed).toBe(true)
  expect(ended).toHaveBeenCalledTimes(1)
})

it('opens no stream for a Run that is not running and closes the stream when the selection leaves it', async () => {
  const done = { ...runningRun([]), run_id: 3, status: 'completed', end_time: '2026-10-21T00:00:05Z' }
  const state = reactive({ runs: [done] })
  mountRuns(state)
  await nextTick()
  expect(FakeEventSource.opened).toHaveLength(0)

  state.runs = [done, runningRun([])]
  await nextTick()
  document.querySelectorAll('tbody tr')[1].click()
  await nextTick()
  expect(FakeEventSource.opened).toHaveLength(1)
  document.querySelectorAll('tbody tr')[0].click()
  await nextTick()
  expect(FakeEventSource.opened[0].closed).toBe(true)
})
