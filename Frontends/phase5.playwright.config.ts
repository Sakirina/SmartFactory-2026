import { defineConfig } from '@playwright/test';
export default defineConfig({
  testDir: './tests', testMatch: 'phase5-*.spec.ts', timeout: 60000, expect: { timeout: 12000 }, workers: 1,
  outputDir: process.env.SF_PHASE5_OUTPUT || '/private/tmp/smartfactory-release-interface-20261005/browser-first/test-results',
  reporter: [['list'], ['json', { outputFile: process.env.SF_PHASE5_REPORT || '/private/tmp/smartfactory-release-interface-20261005/browser-first/results.json' }]],
  use: { browserName: 'chromium', actionTimeout: 12000, navigationTimeout: 20000, launchOptions: { executablePath: process.env.SF_BROWSER_EXECUTABLE || '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome' }, screenshot: 'only-on-failure', trace: 'off' },
});
