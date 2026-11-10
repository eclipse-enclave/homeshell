import { defineConfig } from 'vitest/config';

// Unit specs for the e2e helpers. Only `src/*.spec.ts`: the Playwright specs
// live under src/api and src/ui and never run under vitest.
export default defineConfig(() => ({
  root: __dirname,
  cacheDir: '../../node_modules/.vite/apps/web-e2e',
  test: {
    name: '@homeshell/web-e2e',
    watch: false,
    environment: 'node',
    include: ['src/*.spec.ts'],
    reporters: ['default'],
  },
}));
