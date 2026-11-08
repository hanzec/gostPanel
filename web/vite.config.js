import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import path from 'path'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue()],
  resolve: {
    alias: {
      '@': path.resolve(__dirname, 'src')
    }
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:39100'
      }
    }
  },
  build: {
    outDir: '../internal/router/dist',
    emptyOutDir: true
  },
  test: {
    environment: 'jsdom',
    // Vite must resolve the validator's ESM entry for real Element Plus form validation.
    server: {
      deps: { inline: ['element-plus', 'async-validator'] }
    },
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{js,vue}'],
      reporter: ['text', 'json-summary', 'html'],
      thresholds: { statements: 90 }
    }
  }
})
