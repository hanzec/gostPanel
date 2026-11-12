import { fileURLToPath } from 'node:url'
import webConfig from '../../web/vite.config.js'
import webPackage from '../../web/package.json' with { type: 'json' }

const testDirectory = fileURLToPath(new URL('.', import.meta.url))

export default {
  ...webConfig,
  root: fileURLToPath(new URL('../../web', import.meta.url)),
  resolve: {
    ...webConfig.resolve,
    // Tests outside web still use the dependencies installed for the frontend.
    dedupe: Object.keys({ ...webPackage.dependencies, ...webPackage.devDependencies })
  },
  server: {
    ...webConfig.server,
    fs: { allow: [...webConfig.server.fs.allow, testDirectory] }
  },
  test: {
    dir: testDirectory,
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
}
