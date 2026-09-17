import { afterEach, expect, it } from 'vitest'
import { createApp, nextTick } from 'vue'
import { sync, useLoad } from '../src/core/load.js'

let app
afterEach(() => { app?.unmount(); app = null; document.body.innerHTML = '' })
function deferred() { let resolve, reject; const promise = new Promise((a,b) => {resolve=a;reject=b}); return {promise,resolve,reject} }
function mount(load) { let state; const root=document.createElement('div');document.body.append(root); app=createApp({setup(){state=useLoad(load);return()=>null}});app.mount(root);return state }
it('只有最新請求可以更新資料、錯誤與 loading',async()=>{
  const a=deferred(),b=deferred();let calls=0
  const state=mount(()=>++calls===1?a.promise:b.promise)
  const second=state.reload()
  a.resolve('old');await nextTick();await nextTick()
  expect(state.loading.value).toBe(true)
  expect(state.data.value).toBeNull()
  b.resolve('new');await second
  expect(state.data.value).toBe('new')
  expect(state.loading.value).toBe(false)
})
it('晚到的失敗不能覆蓋新成功，卸載後不更新全域時間',async()=>{
  const a=deferred(),b=deferred(),c=deferred();let calls=0
  const state=mount(()=>[a,b,c][calls++].promise)
  const second=state.reload();b.resolve('current');await second
  a.reject(new Error('old error'));await nextTick();await nextTick()
  expect(state.error.value).toBeNull()
  const at=sync.at;const third=state.reload();app.unmount();app=null
  c.resolve('disposed');await third
  expect(state.data.value).toBe('current');expect(sync.at).toBe(at)
})
