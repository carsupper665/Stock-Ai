import { reactive } from 'vue'

// 破壞性操作一律先開確認框、逐條列出後果（REQUIREMENTS X-08）。
// confirm({ title, lines, ok, tone, input }) → Promise：取消回 null；有 input 回輸入值，否則回 true。
export const dialog = reactive({ open: false, title: '', lines: [], ok: '確認', tone: 'neg', input: null, value: '', resolve: null })

export function confirm({ title, lines = [], ok = '確認', tone = 'neg', input = null }) {
  return new Promise((resolve) => {
    Object.assign(dialog, { open: true, title, lines, ok, tone, input, value: input?.value ?? '', resolve })
  })
}

export function settle(result) {
  const { resolve } = dialog
  Object.assign(dialog, { open: false, resolve: null, value: '' })
  resolve?.(result)
}
