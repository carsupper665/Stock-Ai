import { reactive } from 'vue'

// 破壞性操作一律先開確認框、逐條列出後果（REQUIREMENTS X-08）。
// 取消回 null；fields 回欄位物件；單一 input 回輸入值；一般確認回 true。
export const dialog = reactive({ open: false, title: '', lines: [], ok: '確認', tone: 'neg', input: null, value: '', fields: [], values: {}, resolve: null })

export function confirm({ title, lines = [], ok = '確認', tone = 'neg', input = null, fields = [] }) {
  return new Promise((resolve) => {
    Object.assign(dialog, { open: true, title, lines, ok, tone, input, value: input?.value ?? '', fields, values: Object.fromEntries(fields.map((field) => [field.name, field.value ?? ''])), resolve })
  })
}

export function settle(result) {
  const { resolve } = dialog
  Object.assign(dialog, { open: false, resolve: null, value: '', fields: [], values: {} })
  resolve?.(result)
}
