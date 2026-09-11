const modules = import.meta.glob('../pages/*/index.js', { eager: true })

export const routes = Object.values(modules).map((m) => m.default)
export const nav = routes.filter((r) => r.meta?.nav).sort((a, b) => a.meta.nav - b.meta.nav)
