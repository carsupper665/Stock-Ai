import { reactive } from 'vue'

// 非破壞性操作的回饋：底部居中、2.6 秒自動消失
export const toast = reactive({ text: '', tone: 'acc' })
let timer

export function say(text, tone = 'acc') {
  Object.assign(toast, { text, tone })
  clearTimeout(timer)
  timer = setTimeout(() => (toast.text = ''), 2600)
}

export async function copy(text, label = '已複製') {
  try {
    await navigator.clipboard.writeText(text)
    say(label)
  } catch {
    say('無法存取剪貼簿', 'neg')
  }
}
