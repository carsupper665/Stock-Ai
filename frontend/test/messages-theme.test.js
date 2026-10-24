import { readFileSync } from 'node:fs'
import { expect, it } from 'vitest'

// 群聊改版新增的元件只用 theme.css 的變數，亮色與暗色才會一致
const files = ['Page.vue', 'MemberList.vue', 'MemberDialog.vue', 'Composer.vue'].map((name) => `src/pages/messages/${name}`).concat('src/shared/Avatar.vue')

it('F9 群聊新增的 .vue 檔沒有寫死的 # 色碼、rgb() 或 hsl()', () => {
  for (const file of files) {
    const source = readFileSync(file, 'utf8')
    expect(source.match(/#[0-9a-fA-F]{3,8}\b|\brgba?\(|\bhsla?\(/g), file).toBeNull()
  }
})
