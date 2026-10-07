import { readFile, writeFile } from 'node:fs/promises';
import { closeSync, openSync } from 'node:fs';
import { execFileSync, spawn } from 'node:child_process';
import { evidence } from './phase5-support';
export const releaseProcessRoot = process.env.SF_PHASE5_RELEASE_PROCESS_ROOT || '/private/tmp/smartfactory-phase5-release-process-tenth-20261005';
export const nodeProcessRoot = process.env.SF_PHASE5_NODE_PROCESS_ROOT || '/private/tmp/smartfactory-phase5-node-frontend-live-20261005';
export async function releaseAgentPID(node: 'a' | 'b') {
  const entries = JSON.parse(await readFile(releaseProcessRoot + '/evidence/processes.json', 'utf8'));
  const process = [...entries].reverse().find((entry: any) => entry.name === 'agent-' + node && entry.argv);
  if (!process) throw new Error('The registered release agent is missing');
  return Number(process.pid);
}
export function signalFixture(pid: number, expectedPath: string, signal: NodeJS.Signals) {
  const command = execFileSync('ps', ['-p', String(pid), '-o', 'command='], { encoding: 'utf8' }).trim();
  if (!command.includes(expectedPath)) throw new Error('The PID no longer belongs to the registered private fixture');
  process.kill(pid, signal);
  return { pid, signal, checked_command: command, at_ms: Date.now() };
}
export async function restartNodeConsumer(node: 'a' | 'b') {
  const processFile = nodeProcessRoot + '/private/running-processes.json';
  const entries = JSON.parse(await readFile(processFile, 'utf8'));
  const entry = entries.find((item: any) => item.name === 'node-' + node);
  const command = execFileSync('ps', ['-p', String(entry.pid), '-o', 'command='], { encoding: 'utf8' }).trim();
  if (!command.includes(nodeProcessRoot + '/private/' + node + '.json')) throw new Error('The node consumer no longer belongs to this fixture');
  const argv = command.split(/\s+/);
  signalFixture(entry.pid, nodeProcessRoot + '/private/' + node + '.json', 'SIGTERM');
  for (let attempt = 0; attempt < 30; attempt++) {
    try { process.kill(entry.pid, 0); await new Promise(resolve => setTimeout(resolve, 100)); } catch { break; }
  }
  const out = openSync(evidence + '/node-' + node + '-restart.stdout.log', 'a', 0o600), err = openSync(evidence + '/node-' + node + '-restart.stderr.log', 'a', 0o600);
  const child = spawn(argv[0], argv.slice(1), { detached: true, stdio: ['ignore', out, err] }); child.unref(); closeSync(out); closeSync(err);
  const previous = entry.pid; entry.pid = child.pid; entry.frontend_restart_ms = Date.now();
  await writeFile(processFile, JSON.stringify(entries, null, 2));
  const settings = JSON.parse(await readFile(nodeProcessRoot + '/private/' + node + '.json', 'utf8'));
  const deadline = Date.now() + 15000; let ready = false;
  while (Date.now() < deadline) {
    try { const response = await fetch('http://' + settings.address + '/runtime', { signal: AbortSignal.timeout(1000) }); if (response.status === 200) { ready = true; break; } } catch {}
    await new Promise(resolve => setTimeout(resolve, 100));
  }
  if (!ready) throw new Error('The restarted isolated consumer did not open its runtime observation endpoint');
  return { previous_pid: previous, pid: child.pid, argv, restarted_ms: entry.frontend_restart_ms };
}
export async function restartReleaseAgent(node: 'a' | 'b', binary?: string) {
  const processFile = releaseProcessRoot + '/evidence/processes.json';
  const entries = JSON.parse(await readFile(processFile, 'utf8'));
  const entry = [...entries].reverse().find((item: any) => item.name === 'agent-' + node && item.argv);
  const command = execFileSync('ps', ['-p', String(entry.pid), '-o', 'command='], { encoding: 'utf8' }).trim();
  if (!command.includes(releaseProcessRoot + '/private/agent-' + node)) throw new Error('The release agent no longer belongs to this fixture');
  signalFixture(entry.pid, releaseProcessRoot + '/private/agent-' + node, 'SIGTERM');
  for (let attempt = 0; attempt < 100; attempt++) { try { process.kill(entry.pid, 0); await new Promise(resolve => setTimeout(resolve, 100)); } catch { break; } }
  const out = openSync(evidence + '/agent-' + node + '-restart.stdout.log', 'a', 0o600), err = openSync(evidence + '/agent-' + node + '-restart.stderr.log', 'a', 0o600);
  const argv = [binary || entry.argv[0], ...entry.argv.slice(1)];
  const child = spawn(argv[0], argv.slice(1), { detached: true, stdio: ['ignore', out, err] }); child.unref(); closeSync(out); closeSync(err);
  const current = { ...entry, argv, pid: child.pid, frontend_restart_ms: Date.now() };
  entries.push(current); await writeFile(processFile, JSON.stringify(entries, null, 2));
  return { previous_pid: entry.pid, pid: child.pid, argv: current.argv, restarted_ms: current.frontend_restart_ms };
}
