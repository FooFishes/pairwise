import path from 'node:path'
import tailwindcss from '@tailwindcss/vite'
import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
  },
  build: {
    // 产物内嵌进二进制由本机加载, 不受公网首屏体积约束; mermaid 已按需动态加载.
    chunkSizeWarningLimit: 2048,
  },
})
