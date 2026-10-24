// 成員：群聊裡的一個交易帳戶，以綁定的 Session name 顯示（CONTEXT.md）。

export const DELETED_NAME = '被刪除用戶'

const statusOrder = { active: 0, waiting: 1, stopped: 2 }
const byName = (a, b) => a.name.localeCompare(b.name, 'zh-Hant')

// active＝Session running 且正在跑 Run；waiting＝running 但在等下一個 Run；stopped＝已停止。
function statusOf(session) {
  if (session.status !== 'running') return 'stopped'
  return session.current_run ? 'active' : 'waiting'
}

const isDeleted = (session) => session.status === 'deleted' || Boolean(session.deleted_at)

// sessions 要含已刪除的 Session：綁定不會釋放，帳戶綁過已刪除的 Session 就不再列為成員。
export function buildMembers(accounts, sessions) {
  const sessionOf = new Map()
  for (const session of sessions) {
    if (!sessionOf.has(session.account_id) || isDeleted(sessionOf.get(session.account_id))) sessionOf.set(session.account_id, session)
  }
  const members = []
  const unbound = []
  const byUserName = new Map()
  for (const account of accounts) {
    const session = sessionOf.get(account.id)
    if (session && isDeleted(session)) {
      byUserName.set(account.user_name, { name: DELETED_NAME, sub: account.user_name })
      continue
    }
    const member = session
      ? { id: account.id, account, session, name: session.name, sub: account.user_name, status: statusOf(session) }
      : { id: account.id, account, session: null, name: account.user_name, sub: '', status: null }
    ;(session ? members : unbound).push(member)
    byUserName.set(account.user_name, member)
  }
  members.sort((a, b) => statusOrder[a.status] - statusOrder[b.status] || byName(a, b))
  unbound.sort(byName)
  return { members, unbound, byUserName }
}

// 留言作者的主名稱、小字與是否為 USER。後端對 USER 回 "you"，帳戶已刪除回 "[deleted]"。
export function authorOf(message, directory, userName) {
  if (message.user_name === 'you') return { name: userName, sub: '', user: true }
  if (message.user_name === '[deleted]') return { name: DELETED_NAME, sub: '', user: false }
  const known = directory.byUserName.get(message.user_name)
  return { name: known?.name ?? message.user_name, sub: known?.sub ?? '', user: false }
}

export function tagLabel(tag, directory, userName) {
  if (tag === 'USER') return userName
  return directory.byUserName.get(tag)?.name ?? tag
}

export const mentionsUser = (message) => message.tags.includes('USER')
