import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import ts from 'typescript';

const sourcePath = fileURLToPath(new URL('../../contracts/1.0/api-types.ts', import.meta.url));
test('official generated types retain nullable collections, input parameters, response variants and failure statuses', async () => {
  const directory = await mkdtemp(join(tmpdir(), 'smartfactory-generated-types-'));
  try {
    const fixture = directory + '/contract.ts';
    const source = `import type { components, operations } from ${JSON.stringify(sourcePath.slice(0, -3))};
      const action: components['schemas']['TaskAction'] = { expected_version: 'opaque:9007199254740993' };
      // @ts-expect-error Task action versions remain opaque strings.
      const numericAction: components['schemas']['TaskAction'] = { expected_version: 1 };
      const integer: components['schemas']['Task']['river_id'] = 9223372036854775807n;
      const responseInteger: components['schemas']['Task']['river_id'] = '9223372036854775807';
      const points: components['schemas']['SimulationRequest']['points'] = null;
      const query: operations['get_tasks']['parameters'] = { query: { after: 'task:a/1', limit: 100 } };
      // @ts-expect-error Resource paths use textual identities.
      const badPath: operations['get_tasks_id']['parameters'] = { path: { id: 42 } };
      type SimulationSuccess = operations['post_drafts_id_simulate']['responses'][200]['content']['application/json'];
      const legacy: SimulationSuccess = { definition_id: 'counter', trigger: false };
      const sequence: SimulationSuccess = { results: [], final_state: {} };
      type Conflict = operations['post_tasks_id_retry']['responses'][409]['content']['application/json'];
      type Denied = operations['post_tasks_id_cancel']['responses'][403]['content']['application/json'];
      type Expired = operations['post_drafts_id_simulate']['responses'][401]['content']['application/json'];
      const conflict: Conflict = { error: 'task version changed' };
      const denied: Denied = { error: 'resource access denied' };
      const expired: Expired = { error: 'authentication required' };
      type AcceptedJob = operations['post_jobs_id_retry']['responses'][202]['content']['application/json'];
      const accepted: AcceptedJob = { id: 'job:a/1', task_id: 'recompute:job:a/1:1', cursor_ms: 1800000000000, device_id: 'counter-1', from_ms: 1800000000000, to_ms: 1800000002000, kind: 'recompute', progress: 0, reason: 'retry', status: 'pending', version: 1 };
      // @ts-expect-error Legacy job retries report their accepted result with HTTP 202.
      type WrongJobStatus = operations['post_jobs_id_retry']['responses'][200];
    `;
    await writeFile(fixture, source);
    const program = ts.createProgram([fixture], { strict: true, noEmit: true, skipLibCheck: true, target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ESNext, moduleResolution: ts.ModuleResolutionKind.Bundler, types: [] });
    const diagnostics = ts.getPreEmitDiagnostics(program);
    assert.equal(diagnostics.length, 0, ts.formatDiagnosticsWithColorAndContext(diagnostics, { getCurrentDirectory: () => directory, getCanonicalFileName: value => value, getNewLine: () => '\n' }));
    const api = createRequire(import.meta.resolve('openapi-typescript'))('typescript');
    assert.equal(api.version, '6.0.2');
    assert.equal(api, ts);
    const schema = JSON.parse(await readFile(new URL('../../contracts/1.0/openapi.json', import.meta.url), 'utf8'));
    const output = await readFile(sourcePath, 'utf8');
    const routesText = await readFile(new URL('../src/generated-routes.ts', import.meta.url), 'utf8');
    const routeNames = [...routesText.matchAll(/^  "([^"]+)": \{/gm)].map(match => match[1]);
    assert.equal(Object.values(schema.paths).reduce((count, methods) => count + Object.values(methods).filter(value => value.operationId).length, 0), routeNames.length);
    assert.ok(routeNames.includes('post_queries_kind_events') && routeNames.includes('post_work_orders_id_handovers') && routeNames.includes('get_investigations_id_evidence_evidence_id'));
    assert.match(output, /Generated from contracts\/1.0\/openapi.json by openapi-typescript 7.13.0/);
  } finally { await rm(directory, { force: true, recursive: true }); }
});
