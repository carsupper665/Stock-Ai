import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

const backendHttpTarget = process.env.VITE_DEV_PROXY_TARGET ?? 'http://localhost:7794'
const backendWsTarget = process.env.VITE_DEV_PROXY_WS_TARGET ?? backendHttpTarget.replace(/^http/, 'ws')

export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/__api': {
        target: backendHttpTarget,
        rewrite: (path) => path.replace(/^\/__api/, ''),
      },
      '/ws': {
        target: backendWsTarget,
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: './src/tests/setup.ts',
  },
})
