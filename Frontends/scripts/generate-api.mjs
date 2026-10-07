import { readFile, writeFile } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { createRequire } from 'node:module';
import openapiTS, { astToString } from 'openapi-typescript';
import ts from 'typescript';

const schemaURL = new URL('../../contracts/1.0/openapi.json', import.meta.url);
const source = await readFile(schemaURL, 'utf8');
const schema = JSON.parse(source);
const generatorRequire = createRequire(import.meta.resolve('openapi-typescript'));
const generatorTS = generatorRequire('typescript');
if (ts.version !== '6.0.2' || generatorTS.version !== ts.version || typeof ts.transpileModule !== 'function') {
  throw new Error(`Compiler API mismatch: script=${ts.version}, openapi-typescript=${generatorTS.version}`);
}
const ast = await openapiTS(schema, {
  alphabetize: true,
  transform(value) {
    // The shared API decoder keeps unsafe integers as strings. Sequence
    // simulation also retains numeric provenance as bigint for replay/export.
    if (value.type === 'integer' && ['int64', 'uint64'].includes(value.format)) {
      return ts.factory.createUnionTypeNode([
        ts.factory.createKeywordTypeNode(ts.SyntaxKind.NumberKeyword),
        ts.factory.createKeywordTypeNode(ts.SyntaxKind.StringKeyword),
        ts.factory.createKeywordTypeNode(ts.SyntaxKind.BigIntKeyword),
      ]);
    }
  },
});
const digest = createHash('sha256').update(source).digest('hex');
const header = `// Generated from contracts/1.0/openapi.json by openapi-typescript 7.13.0. Do not edit.\n// Source SHA-256: ${digest}\n`;
const routes = {};
for (const [path, methods] of Object.entries(schema.paths).sort(([a], [b]) => a.localeCompare(b))) {
  for (const [method, operation] of Object.entries(methods).sort(([a], [b]) => a.localeCompare(b))) {
    if (!['get', 'post', 'put', 'patch', 'delete', 'options', 'head'].includes(method)) continue;
    if (!operation.operationId || Object.hasOwn(routes, operation.operationId)) throw new Error(`Missing or duplicate operationId at ${path}`);
    routes[operation.operationId] = { method: method.toUpperCase(), path };
  }
}
const outputs = new Map([
  [new URL('../../contracts/1.0/api-types.ts', import.meta.url), header + astToString(ast)],
  [new URL('../src/generated-routes.ts', import.meta.url), header + `export const operationRoutes = ${JSON.stringify(routes, null, 2)} as const;\n`],
]);
for (const [url, content] of outputs) {
  if (process.argv.includes('--check')) {
    if (await readFile(url, 'utf8') !== content) throw new Error(`Generated contract differs: ${url.pathname}; run npm run generate:api`);
  } else await writeFile(url, content);
}
console.log(JSON.stringify({ source_sha256: digest, schemas: Object.keys(schema.components.schemas).length, operations: Object.keys(routes).length, compiler_api: generatorTS.version, checked: process.argv.includes('--check') }));
