import { describe, expect, it } from 'vitest'

import { nav, routes } from '../src/core/pages.js'

describe('頁面包契約：pages/*/index.js', () => {
  it('每一包都是完整的 route record，Page.vue 載得起來', async () => {
    expect(routes.length).toBeGreaterThan(0)
    for (const r of routes) {
      expect(r.path, `${r.name} 的 path`).toMatch(/^\//)
      expect(typeof r.name, `${r.path} 的 name`).toBe('string')
      expect(typeof r.component, `${r.name} 的 component`).toBe('function')
      const page = await r.component()
      expect(page.default, `${r.name} 的 Page.vue`).toBeTruthy()
    }
  })

  it('name 不重複', () => {
    const names = routes.map((r) => r.name)
    expect(new Set(names).size).toBe(names.length)
  })

  it('只有 login 是公開頁', () => {
    expect(routes.filter((r) => r.meta?.public).map((r) => r.name)).toEqual(['login'])
  })

  it('選單只收有 nav 的頁面並照 nav 排序', () => {
    expect(nav.every((r) => r.meta.nav && r.meta.title)).toBe(true)
    expect(nav.map((r) => r.meta.nav)).toEqual([...nav.map((r) => r.meta.nav)].sort((a, b) => a - b))
  })
})
