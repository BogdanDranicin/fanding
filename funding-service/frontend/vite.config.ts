import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Куда dev-сервер проксирует запросы. По умолчанию — локальный бэкенд; через
// VITE_API_TARGET можно направить фронт на боевой, чтобы смотреть вёрстку на
// настоящих данных, не поднимая у себя базу и сбор.
const apiTarget = process.env.VITE_API_TARGET ?? 'http://localhost:8080'
const wsTarget = apiTarget.replace(/^http/, 'ws')

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
      '/ws': { target: wsTarget, changeOrigin: true, ws: true },
    },
  },
})
