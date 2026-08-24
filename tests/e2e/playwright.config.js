import { defineConfig } from '@playwright/test'

const baseURL = process.env.PLAYWRIGHT_BASE_URL
if (!baseURL) {
  throw new Error(
    'PLAYWRIGHT_BASE_URL must be set (use ./devmail.sh test:dev or test:prod)',
  )
}

export default defineConfig({
  testDir: './specs',
  // The mailbox is one shared in-memory store; parallel specs would see each
  // other's messages and each other's clears.
  fullyParallel: false,
  workers: 1,
  reporter: 'list',
  use: {
    baseURL,
    trace: 'retain-on-failure',
  },
})
