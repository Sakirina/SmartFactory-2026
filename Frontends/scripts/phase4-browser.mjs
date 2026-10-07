import { spawnSync } from 'node:child_process';
import { mkdirSync, readFileSync, writeFileSync, copyFileSync, cpSync, existsSync } from 'node:fs';
import path from 'node:path';
const runtime = process.env.SF_PHASE4_RUNTIME || '/private/tmp/smartfactory-frontend-phase4-20261005';
const reports = process.env.SF_PHASE4_REPORT_DIR || path.join(runtime, 'reports');
const groups = {
  business: ['phase4.spec.ts', 'browser-business-final'],
  details: ['phase4-business-details.spec.ts', 'browser-details-final'],
  state: ['phase4-state.spec.ts', 'browser-state-final'],
  precision: ['phase4-precision.spec.ts', 'browser-precision-final'],
  surfaces: ['phase4.spec.ts', 'browser-surfaces-final'],
  ai: ['phase4-ai.spec.ts', 'browser-ai-final'],
};
const selected = process.argv.slice(2);
const order = selected.length ? selected : Object.keys(groups);
if (order.some(group => !groups[group])) throw new Error('Unknown phase4 browser group');
mkdirSync(reports, { recursive: true });
const results = [];
for (const group of order) {
  const [file, folder] = groups[group];
  const env = { ...process.env, SF_PHASE4_RUNTIME: runtime, SF_PHASE4_REPORT_DIR: reports, SF_PHASE4_CASES: group,
    SF_PHASE4_DATA_SUFFIX: 'final', SF_PHASE4_EVIDENCE: path.join(runtime, folder),
    SF_PHASE4_AI_DIR: process.env.SF_PHASE4_AI_DIR || 'ai-final' };
  if (['precision', 'surfaces'].includes(group)) Object.assign(env, { SF_PHASE4_DATA_SUFFIX: 'precision',
    SF_PHASE4_CLOUD: process.env.SF_PHASE4_PRECISION_CLOUD || 'http://127.0.0.1:19413',
    SF_PHASE4_EDGE: process.env.SF_PHASE4_PRECISION_EDGE || 'http://127.0.0.1:19414', SF_PHASE4_SURFACE_USER: 'admin' });
  const start = new Date().toISOString();
  const run = spawnSync(process.execPath, ['node_modules/@playwright/test/cli.js', 'test', '--config=playwright.phase4.config.ts',
    file, '--timeout=45000'], { env, encoding: 'utf8', maxBuffer: 4 * 1024 * 1024 });
  const log = path.join(reports, 'browser-' + group + '.log');
  writeFileSync(log, (run.stdout || '') + (run.stderr || '') + (run.error ? String(run.error) : ''));
  const output = path.join(reports, 'browser-results.json');
  const saved = path.join(reports, 'browser-results-' + group + '.json');
  if (existsSync(output)) copyFileSync(output, saved);
  const contexts = path.join(runtime, 'playwright-results');
  if (existsSync(contexts)) cpSync(contexts, path.join(runtime, 'playwright-results-' + group), { recursive: true });
  const parsed = existsSync(saved) ? JSON.parse(readFileSync(saved, 'utf8')) : {};
  const result = { group, start, end: new Date().toISOString(), exit_code: run.status, log, result: saved, statistics: parsed.stats };
  results.push(result);
  writeFileSync(path.join(reports, 'runner-results.json'), JSON.stringify(results, null, 2) + '\n');
  process.stdout.write(JSON.stringify(result) + '\n');
  if (run.status !== 0) process.exit(run.status || 1);
}
