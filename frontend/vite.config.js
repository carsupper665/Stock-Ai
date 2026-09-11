import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue()],
  server: { proxy: { '/v1': 'http://localhost:7794' } },
  test: { environment: 'happy-dom', include: ['test/**/*.test.js'] },
})
