import { afterEach, expect, it, vi } from 'vitest'
import { createApp, h, nextTick } from 'vue'
import MessagesPage from '../src/pages/messages/Page.vue'
import Confirm from '../src/shared/Confirm.vue'
import { confirm, dialog, settle } from '../src/shared/confirm.js'
import { session } from '../src/core/session.js'
import { toast } from '../src/shared/toast.js'

let app
const account = { id: 'a', user_name: 'Alpha', status: 'active', chat_read: true, chat_post: true, chat_muted_until: null, chat_mute_reason: null, chat_rate_minutes: 1, chat_rate_count: 3 }
const respond = (body, status = 200) => new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const button = (label, root = document) => [...root.querySelectorAll('button')].find((x) => x.textContent === label)
function mount() {
 session.token = 'user-token'
 const root = document.createElement('div'); document.body.append(root)
 app = createApp({ render: () => h('div', [h(MessagesPage), h(Confirm)]) }); app.mount(root)
}
function input(el, value) { el.value = value; el.dispatchEvent(new Event('input', { bubbles: true })) }
function setup() {
 let state = { ...account }
 return vi.spyOn(globalThis, 'fetch').mockImplementation(async (url, request) => {
  if (url === '/v1/accounts') return respond({ accounts: [state, { ...account, id: 'b', user_name: 'Beta', status: 'disabled' }] })
  if (request.method === 'POST' && url.endsWith('/chat/mute')) { const body = JSON.parse(request.body); state = { ...state, chat_muted_until: new Date(Date.now() + body.minutes * 60000).toISOString(), chat_mute_reason: body.reason }; return respond(state) }
  if (request.method === 'DELETE') { state = { ...state, chat_muted_until: null, chat_mute_reason: null }; return respond(state) }
  return respond({ messages: [], page: 1, has_more: false })
 })
}
afterEach(() => { app?.unmount(); app = null; settle(null); document.body.innerHTML = ''; toast.text = ''; vi.restoreAllMocks(); vi.useRealTimers() })

it('成員管理列出全部帳號、狀態、權限與頻率', async () => {
 setup(); mount()
 await vi.waitFor(() => expect(document.querySelectorAll('.member')).toHaveLength(2))
 const panel = document.querySelector('.members')
 expect(panel.textContent).toContain('成員管理')
 expect(panel.textContent).toContain('Alpha'); expect(panel.textContent).toContain('Beta')
 expect(panel.textContent).toContain('active'); expect(panel.textContent).toContain('disabled')
 expect(panel.querySelectorAll('select')).toHaveLength(2)
 expect([...panel.querySelectorAll('input[type=number]')].map((x) => x.value)).toEqual(['1', '3', '1', '3'])
})

it('禁言對話框兩欄必填、整數與範圍驗證，POST 後立即顯示並可 DELETE 解除', async () => {
 const fetch = setup(); mount()
 await vi.waitFor(() => expect(button('禁言')).toBeTruthy())
 button('禁言').click(); await nextTick()
 const form = document.querySelector('dialog form')
 const minutes = form.querySelector('input[type=number]'), reason = form.querySelector('input[type=text]')
 const submit = form.querySelector('button:not([type=button])')
 expect(submit.disabled).toBe(true)
 input(minutes,'10'); await nextTick(); expect(submit.disabled).toBe(true)
 input(reason,'   '); await nextTick(); expect(submit.disabled).toBe(true)
 input(reason,'洗版'); input(minutes,'1.5'); await nextTick(); expect(submit.disabled).toBe(true)
 input(minutes,'43201'); await nextTick(); expect(submit.disabled).toBe(true)
 input(minutes,'10'); await nextTick(); expect(submit.disabled).toBe(false)
 form.dispatchEvent(new Event('submit',{bubbles:true,cancelable:true}))
 await vi.waitFor(() => expect(document.querySelector('.member').textContent).toContain('禁言中 · 剩 10 分鐘 · 洗版'))
 const [url,request] = fetch.mock.calls.find(([,r]) => r.method === 'POST')
 expect(url).toBe('/v1/accounts/a/chat/mute'); expect(JSON.parse(request.body)).toEqual({minutes:10,reason:'洗版'})
 button('解除').click()
 await vi.waitFor(() => expect(button('解除')).toBeUndefined())
 expect(fetch.mock.calls.some(([url,r])=>url==='/v1/accounts/a/chat/mute' && r.method==='DELETE')).toBe(true)
 expect(button('禁言')).toBeTruthy()
})

it('頻率失焦或 Enter 才 PATCH 一次，只含改動欄位；後端失敗復原', async () => {
 const fetch = setup()
 fetch.mockImplementation(async (url,request) => {
  if (url === '/v1/accounts') return respond({accounts:[account]})
  if (request.method === 'PATCH') {
   const body = JSON.parse(request.body)
   if (body.chat_rate_count === 101) return respond({error:'invalid_request',message:'chat_rate_count 必須是 1～100 的整數'},400)
   return respond({...account,...body})
  }
  return respond({messages:[],page:1,has_more:false})
 })
 mount(); await vi.waitFor(() => expect(document.querySelector('.rate input')).toBeTruthy())
 const [minutes,count] = document.querySelectorAll('.rate input')
 minutes.focus(); input(minutes,'2'); await nextTick()
 expect(fetch.mock.calls.filter(([,r])=>r.method==='PATCH')).toHaveLength(0)
 minutes.blur()
 await new Promise((resolve) => setTimeout(resolve, 0))
 expect(minutes.disabled).toBe(false)
 expect(fetch.mock.calls.filter(([,r])=>r.method==='PATCH').map(([,r])=>JSON.parse(r.body))).toEqual([{chat_rate_minutes:2}])
 count.focus(); input(count,'1'); count.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter',bubbles:true})); await nextTick()
 await new Promise((resolve) => setTimeout(resolve, 0))
 expect(count.disabled).toBe(false)
 count.dispatchEvent(new FocusEvent('blur'))
 expect(fetch.mock.calls.filter(([,r])=>r.method==='PATCH').map(([,r])=>JSON.parse(r.body))).toEqual([{chat_rate_minutes:2},{chat_rate_count:1}])
 count.focus(); input(count,'101'); count.blur()
 await vi.waitFor(() => expect(toast.text).toBe('chat_rate_count 必須是 1～100 的整數'))
 expect(count.value).toBe('1')
})

it('每 30 秒更新剩餘分鐘，到期自動顯示禁言按鈕；每 5 秒更新帳號清單', async () => {
 vi.useFakeTimers({toFake:['Date','setInterval','clearInterval']})
 const until = new Date(Date.now()+61000).toISOString()
 const fetch = vi.spyOn(globalThis,'fetch').mockImplementation(async (url)=>url==='/v1/accounts'
  ? respond({accounts:[{...account,chat_muted_until:until,chat_mute_reason:'理由'}]})
  : respond({messages:[],page:1,has_more:false}))
 mount(); await vi.waitFor(()=>expect(document.querySelector('.member')?.textContent).toContain('剩 2 分鐘'))
 await vi.advanceTimersByTimeAsync(30000); await nextTick()
 expect(document.querySelector('.member').textContent).toContain('剩 1 分鐘')
 expect(fetch.mock.calls.filter(([url])=>url==='/v1/accounts').length).toBeGreaterThanOrEqual(7)
 await vi.advanceTimersByTimeAsync(60000); await nextTick()
 expect(document.querySelector('.member').textContent).not.toContain('禁言中')
 expect(button('解除')).toBeUndefined(); expect(button('禁言')).toBeTruthy()
})

it('多欄位確認擴充保留既有單欄位輸入與一般確認的回傳值', async () => {
 setup(); mount(); await nextTick()
 const single = confirm({title:'單欄位',input:{label:'名稱',value:'原名'}})
 await nextTick(); input(document.querySelector('dialog input'),'新名'); await nextTick()
 document.querySelector('dialog form').dispatchEvent(new Event('submit',{cancelable:true}))
 await expect(single).resolves.toBe('新名')
 const simple = confirm({title:'確認'})
 await nextTick(); document.querySelector('dialog form').dispatchEvent(new Event('submit',{cancelable:true}))
 await expect(simple).resolves.toBe(true)
})

it('延遲的篩選重載不會覆蓋剛成功的禁言狀態', async () => {
 let finishReload
 let reads = 0
 const muted = { ...account, chat_muted_until: new Date(Date.now()+600000).toISOString(), chat_mute_reason:'新原因' }
 vi.spyOn(globalThis,'fetch').mockImplementation(async (url,request)=>{
  if (url === '/v1/accounts') return respond({accounts:[account]})
  if (request.method === 'POST') return respond(muted)
  if (++reads === 2) return new Promise((resolve)=>{finishReload=resolve})
  return respond({messages:[],page:1,has_more:false})
 })
 mount(); await vi.waitFor(()=>expect(button('禁言')).toBeTruthy())
 const filter=document.querySelector('.board select')
 filter.value='USER'; filter.dispatchEvent(new Event('change',{bubbles:true}))
 await vi.waitFor(()=>expect(finishReload).toBeTypeOf('function'))
 button('禁言').click(); await nextTick()
 const form=document.querySelector('dialog form')
 input(form.querySelector('input[type=number]'),'10'); input(form.querySelector('input[type=text]'),'新原因')
 await nextTick(); form.dispatchEvent(new Event('submit',{cancelable:true}))
 await vi.waitFor(()=>expect(document.querySelector('.member').textContent).toContain('禁言中 · 剩 10 分鐘 · 新原因'))
 finishReload(respond({messages:[{id:'reloaded',user_name:'you',content:'重載完成',tags:[],created_at:new Date().toISOString()}],page:1,has_more:false}))
 await vi.waitFor(()=>expect(document.querySelector('.msg')?.textContent).toContain('重載完成'))
 expect(document.querySelector('.member').textContent).toContain('禁言中 · 剩 10 分鐘 · 新原因')
})
