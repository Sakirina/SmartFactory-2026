import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests', timeout: 45000, expect: { timeout: 10000 }, workers: 1,
  reporter: [['list'], ['json', { outputFile: '../.local/evidence/frontend-results.json' }]],
  use: { baseURL: process.env.SF_BROWSER_BASE_URL || 'http://127.0.0.1:8090', browserName: 'chromium', launchOptions: { executablePath: process.env.SF_BROWSER_EXECUTABLE || (process.platform === 'darwin' ? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' : '/usr/bin/chromium') }, screenshot: 'only-on-failure', trace: 'retain-on-failure' },
});
