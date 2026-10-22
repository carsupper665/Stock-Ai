import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv } from 'vite'

// 開發時所有 API 都交給 Middleware（唯一的登入層，需先完成 OIDC 登入取得 session cookie）；
// 它負責驗證並轉給 Backend／Agent／LLM Server，瀏覽器與這個代理都不持有任何服務憑證。
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  if (mode === 'production' && env.VITE_AUTH_PROVIDER && env.VITE_AUTH_PROVIDER !== 'oidc') {
    throw new Error('Production frontend supports only cookie-based OIDC authentication')
  }
  const middleware = env.MIDDLEWARE_URL ?? 'http://127.0.0.1:4180'
  return {
    plugins: [vue()],
    define: mode === 'production' ? { 'import.meta.env.VITE_AUTH_PROVIDER': JSON.stringify('oidc'), 'import.meta.env.VITE_DEV_TOKEN': JSON.stringify('') } : {},
    server: {
      proxy: {
        '/oauth2': middleware,
        '/v1': middleware,
        '/agent': middleware,
        '/llm': middleware,
        '/middleware/healthz': { target: middleware, rewrite: () => '/healthz' },
      },
    },
    test: { environment: 'happy-dom', include: ['test/**/*.test.js'] },
  }
})
