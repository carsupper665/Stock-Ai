import { expect, it } from 'vitest'

import { DELETED_NAME, authorOf, buildMembers, mentionsUser, tagLabel } from '../src/pages/messages/members.js'

const account = (id, user_name) => ({ id, user_name, status: 'active' })
const session = (id, account_id, name, status, current_run = null, deleted_at = null) => ({ id, account_id, name, status, current_run, deleted_at })

const accounts = [
  account('a_run', 'acct-run'), account('a_wait', 'acct-wait'), account('a_stop', 'acct-stop'),
  account('a_wait2', 'acct-wait2'), account('a_free', 'zeta-free'), account('a_free2', 'alpha-free'), account('a_gone', 'acct-gone'),
]
const sessions = [
  session('s_stop', 'a_stop', 'Alpha 停止中', 'stopped'),
  session('s_wait', 'a_wait', 'Zulu 等待', 'running'),
  session('s_wait2', 'a_wait2', 'Bravo 等待', 'running'),
  session('s_run', 'a_run', 'Yankee 執行中', 'running', { run_id: 3 }),
  session('s_gone', 'a_gone', '已刪除的 Session', 'deleted', null, '2026-10-01T00:00:00Z'),
]

it('主名稱用 Session name、帳號名稱放小字；未綁 Session 只顯示帳號名稱', () => {
  const { members, unbound } = buildMembers(accounts, sessions)
  const run = members.find((member) => member.id === 'a_run')
  expect([run.name, run.sub, run.session.id]).toEqual(['Yankee 執行中', 'acct-run', 's_run'])
  expect(unbound.map((member) => [member.name, member.sub, member.session, member.status])).toEqual([
    ['alpha-free', '', null, null], ['zeta-free', '', null, null],
  ])
})

it('三種狀態點依 running+current_run、running、stopped 排序，同狀態依名稱', () => {
  const { members } = buildMembers(accounts, sessions)
  expect(members.map((member) => [member.name, member.status])).toEqual([
    ['Yankee 執行中', 'active'], ['Bravo 等待', 'waiting'], ['Zulu 等待', 'waiting'], ['Alpha 停止中', 'stopped'],
  ])
})

it('Session 已刪除與帳戶已刪除的成員不在清單，它們的訊息顯示「被刪除用戶」', () => {
  const directory = buildMembers(accounts, sessions)
  const listed = [...directory.members, ...directory.unbound].map((member) => member.id)
  expect(listed).not.toContain('a_gone')
  expect(listed).toHaveLength(6)
  expect(authorOf({ user_name: 'acct-gone', tags: [] }, directory, 'Bless')).toEqual({ name: DELETED_NAME, sub: 'acct-gone', user: false })
  expect(authorOf({ user_name: '[deleted]', tags: [] }, directory, 'Bless')).toEqual({ name: DELETED_NAME, sub: '', user: false })
})

it('帳戶以前綁過已刪除的 Session，就算 Session 列表另有紀錄也不列出', () => {
  const directory = buildMembers([account('a', 'acct')], [session('old', 'a', 'Old', 'deleted', null, '2026-10-01T00:00:00Z')])
  expect(directory.members).toEqual([])
  expect(directory.unbound).toEqual([])
})

it('USER 的訊息顯示 USER_NAME 並標記 USER；其他作者用成員名稱', () => {
  const directory = buildMembers(accounts, sessions)
  expect(authorOf({ user_name: 'you', tags: [] }, directory, 'Bless')).toEqual({ name: 'Bless', sub: '', user: true })
  expect(authorOf({ user_name: 'acct-wait', tags: [] }, directory, 'Bless')).toEqual({ name: 'Zulu 等待', sub: 'acct-wait', user: false })
  expect(authorOf({ user_name: 'zeta-free', tags: [] }, directory, 'Bless')).toEqual({ name: 'zeta-free', sub: '', user: false })
  expect(tagLabel('USER', directory, 'Bless')).toBe('Bless')
  expect(tagLabel('acct-run', directory, 'Bless')).toBe('Yankee 執行中')
  expect(mentionsUser({ tags: ['acct-run', 'USER'] })).toBe(true)
  expect(mentionsUser({ tags: ['acct-run'] })).toBe(false)
})
