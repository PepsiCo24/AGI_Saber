import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 开发期：Vite dev server 跑 :5173，把后端接口代理到 :8090（同源，免 CORS）。
// 生产期：npm run build 产出 dist/，由独立静态服务器/nginx 托管，/api 反代到后端。
export default defineConfig({
  plugins: [vue()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://localhost:8090', changeOrigin: true },
      '/healthz': { target: 'http://localhost:8090', changeOrigin: true },
      '/readyz': { target: 'http://localhost:8090', changeOrigin: true },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
})
