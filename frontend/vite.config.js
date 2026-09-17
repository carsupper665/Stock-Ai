import vue from '@vitejs/plugin-vue'
import { defineConfig, loadEnv } from 'vite'

// 三個服務都沒有 CORS，也不該把 Agent／LLM Server 的管理憑證交給瀏覽器：
// 開發時由這裡的代理去掉前綴並注入 Bearer；正式環境由反向代理做同一件事（SPEC §8）。
function upstream(target, prefix, token) {
  return {
    target,
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    rewrite: (path) => path.slice(prefix.length),
    configure: (proxy) =>
      proxy.on('proxyReq', (req) => (token ? req.setHeader('Authorization', `Bearer ${token}`) : req.removeHeader('Authorization'))),
  }
}

export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const agent = env.AGENT_SERVER_URL ?? 'http://127.0.0.1:8080'
  const llm = env.LLM_SERVER_URL ?? 'http://127.0.0.1:8090'
  return {
    plugins: [vue()],
    define: mode === 'production' ? { 'import.meta.env.VITE_DEV_TOKEN': JSON.stringify('') } : {},
    server: {
      proxy: {
        '/v1': env.BACKEND_URL ?? 'http://localhost:7794',
        // 順序有意義：/llm/v1/models 要 runtime 憑證，其餘 /llm 要 admin 憑證
        '/llm/v1/models': upstream(llm, '/llm', env.LLM_SERVER_RUNTIME_TOKEN),
        '/llm': upstream(llm, '/llm', env.LLM_SERVER_ADMIN_TOKEN),
        '/agent': upstream(agent, '/agent', env.AGENT_SERVER_ADMIN_TOKEN),
      },
    },
    test: { environment: 'happy-dom', include: ['test/**/*.test.js'] },
  }
})
