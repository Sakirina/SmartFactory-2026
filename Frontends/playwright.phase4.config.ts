import { defineConfig } from '@playwright/test';
import path from 'node:path';
const runtime = process.env.SF_PHASE4_RUNTIME || '/private/tmp/smartfactory-frontend-phase4-20261005';
const reports = process.env.SF_PHASE4_REPORT_DIR || '../.local/evolution/frontend-phase4';
export default defineConfig({testDir:'./tests',testMatch:'phase4*.spec.ts',timeout:90000,expect:{timeout:15000},workers:1,reporter:[['list'],['json',{outputFile:path.join(reports,'browser-results.json')}]],outputDir:path.join(runtime,'playwright-results'),use:{browserName:'chromium',launchOptions:{executablePath:'/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'},screenshot:'off',trace:'off'}});
