import { expect, it } from 'vitest'

const sources = import.meta.glob('../src/**/*.{vue,css}', { query: '?raw', import: 'default', eager: true })

it('顏色只能寫在 theme.css，頁面一律用 var(--…)', () => {
  const checked = Object.entries(sources).filter(([file]) => !file.endsWith('/theme.css'))
  expect(checked.length).toBeGreaterThan(0)
  for (const [file, src] of checked) {
    expect(src, file).not.toMatch(/#[0-9a-f]{3,8}\b|\brgba?\(|\bhsla?\(/i)
  }
})
