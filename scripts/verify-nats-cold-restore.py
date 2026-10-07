#!/usr/bin/env python3
"""Restore a retained old three-server cold copy into a separate NATS project."""
import argparse
from datetime import datetime, timezone
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--source', type=Path, required=True)
    parser.add_argument('--directory', type=Path, required=True)
    args = parser.parse_args()
    os.umask(0o077)
    source, work = args.source.resolve(), args.directory.resolve()
    work.mkdir(parents=True, exist_ok=False)
    cold = source / 'source-cold-backup'
    manifest = json.loads((cold / 'manifest.json').read_text())
    for item in manifest:
        path = cold / item['path']
        if path.is_symlink() or not path.resolve().is_relative_to(cold) or path.stat().st_size != item['bytes'] or sha(path) != item['sha256']:
            raise ValueError('retained cold-copy file differs: ' + item['path'])
    site = work / '.local/site'
    site.mkdir(parents=True)
    for name in ['pki', 'sqlite']:
        shutil.copytree(source / '.local/site' / name, site / name)
    for name in ['credentials.json', 'nats-1.conf', 'nats-2.conf', 'nats-3.conf']:
        shutil.copy2(source / '.local/site' / name, site / name)
    shutil.copy2(cold / 'expected-checkpoint.json', site / 'persistent-checkpoint.json')
    old_members = json.loads((source / 'source_2.11.9-members.json').read_text())
    old_image = old_members[0]['configured_image']
    configuration = json.loads((source / 'deploy/compose.site.json').read_text())
    project = 'sf-message-cold-restored-20261005'
    configuration['name'] = project
    configuration['volumes'] = {}
    for name, service in configuration['services'].items():
        service['image'] = old_image
        service['labels'] = {'sf.evolution.fixture': project}
        mounts = []
        for mount in service['volumes']:
            original, container, *options = mount.split(':')
            if container == '/data':
                original = str(work / ('data-' + name))
            else:
                old_path = Path(original)
                if not old_path.is_absolute():
                    old_path = (source / 'deploy' / old_path).resolve()
                original = str(work / old_path.relative_to(source))
            mounts.append(':'.join([original, container, *options]))
        service['volumes'] = mounts
        shutil.copytree(cold / name, work / ('data-' + name))
    (work / 'deploy').mkdir()
    compose = work / 'deploy/compose.site.json'
    compose.write_text(json.dumps(configuration, indent=2) + '\n')
    identities = {}
    for path in sorted((site / 'pki').rglob('*')):
        if path.is_file():
            original = source / '.local/site/pki' / path.relative_to(site / 'pki')
            identities[str(path.relative_to(site))] = {'sha256': sha(path), 'source_sha256': sha(original), 'bytes': path.stat().st_size}
    env = dict(os.environ, GOTOOLCHAIN='local', GOPROXY='off', GOMAXPROCS='2',
               GOPATH='/private/tmp/smartfactory-evolution-gopath',
               GOMODCACHE='/private/tmp/smartfactory-evolution-gomodcache',
               GOCACHE='/private/tmp/smartfactory-evolution-go-cache')
    binary = work / 'coordination.test'
    original_test = ROOT / 'Platform/internal/coordination/infrastructure_test.go'
    replacement = work / 'infrastructure_test.go'
    original_text = original_test.read_text()
    old_statement = '\t\texpected.Params["migration"] = "upgraded-server"\n'
    if original_text.count(old_statement) > 1:
        raise ValueError('historical migration fixture input statement differs')
    # The current control state machine keeps request parameters immutable.
    # Preserve that invariant while exercising the historical takeover fixture.
    replacement.write_text(original_text.replace(old_statement, ''))
    overlay = work / 'overlay.json'
    overlay.write_text(json.dumps({'Replace': {str(original_test): str(replacement)}}, indent=2) + '\n')
    raw = subprocess.check_output(['go', 'list', '-overlay', str(overlay), '-deps', '-test', '-json', './internal/coordination'], cwd=ROOT / 'Platform', env=env, text=True)
    (work / 'go-list-inputs.jsonl').write_text(raw)
    decoder, inputs = json.JSONDecoder(), {}
    while raw.strip():
        package, end = decoder.raw_decode(raw.lstrip())
        raw = raw.lstrip()[end:]
        directory = Path(package.get('Dir', '/nonexistent'))
        for field in ['GoFiles', 'CgoFiles', 'CFiles', 'CXXFiles', 'MFiles', 'HFiles', 'FFiles', 'SFiles', 'SwigFiles', 'SwigCXXFiles', 'SysoFiles', 'TestGoFiles', 'XTestGoFiles', 'EmbedFiles', 'TestEmbedFiles', 'XTestEmbedFiles']:
            for name in package.get(field, []):
                path = directory / name
                effective = replacement if path == original_test else path
                if effective.is_file():
                    inputs[str(path)] = {'path': str(effective), 'sha256': sha(effective), 'bytes': effective.stat().st_size}
    for name in ['Platform/go.mod', 'Platform/go.sum', 'DataTransfer/go.mod', 'DataTransfer/go.sum']:
        path = ROOT / name
        inputs[str(path)] = {'path': str(path), 'sha256': sha(path), 'bytes': path.stat().st_size}
    (work / 'compile-inputs-before.json').write_text(json.dumps(inputs, indent=2) + '\n')
    build_command = ['go', 'test', '-overlay', str(overlay), '-c', '-p', '1', '-o', str(binary), './internal/coordination']
    with (work / 'build.log').open('w') as log:
        subprocess.run(build_command, cwd=ROOT / 'Platform', env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
    spec = importlib.util.spec_from_file_location('nats_upgrade', ROOT / 'scripts/check-nats-upgrade.py')
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    fixture = module.Fixture(work, old_image)
    fixture.report.update({'scope': 'independent restore of 2.11.9 source cold backup after target upgrade to 2.14.7',
                           'source_directory': str(source), 'cold_manifest_sha256': sha(cold / 'manifest.json'),
                           'restored_files_before_start': manifest, 'retained_identity_material': identities,
                           'tested_binary': {'path': str(binary), 'sha256': sha(binary), 'bytes': binary.stat().st_size},
                           'build': {'argv': build_command, 'cwd': str(ROOT / 'Platform'), 'environment': {key: env[key] for key in ['GOTOOLCHAIN', 'GOPROXY', 'GOMAXPROCS', 'GOPATH', 'GOMODCACHE', 'GOCACHE']},
                                     'script': str(Path(__file__).resolve()), 'script_sha256': sha(Path(__file__).resolve()), 'log': str(work / 'build.log'), 'log_sha256': sha(work / 'build.log')},
                           'test_input_adaptation': {'original': str(original_test), 'original_sha256': sha(original_test), 'effective': str(replacement), 'effective_sha256': sha(replacement),
                                                     'overlay': str(overlay), 'change': 'Retain original immutable execution request parameters during takeover; production code and other assertions unchanged',
                                                     'historical_statement_removed': old_statement in original_text},
                           'compile_inputs_before': {'path': str(work / 'compile-inputs-before.json'), 'sha256': sha(work / 'compile-inputs-before.json'), 'count': len(inputs)},
                           'original_server_names': [v['varz']['server_name'] for v in old_members]})
    try:
        fixture.dc('up', '-d', '--pull', 'never')
        fixture.verify('restored_old_backup')
        fixture.snapshot('restored_old_backup')
        expected = json.loads((source / 'source_2.11.9-checkpoint.json').read_text())
        actual = json.loads((work / 'restored_old_backup-checkpoint.json').read_text())
        fixture.report['checks']['checkpoint_content_signature_revision_preserved'] = all(actual[key] == expected[key] for key in ['execution', 'checkpoint_revision', 'checkpoint_sha256'])
        for previous, current in zip(expected['streams'], actual['streams']):
            if previous['config'] != current['config'] or previous['state']['messages'] != current['state']['messages'] or previous['state']['first_seq'] != current['state']['first_seq'] or previous['state']['last_seq'] != current['state']['last_seq']:
                raise ValueError('restored stream messages or positions differ from backup')
        fixture.report['checks']['stream_configuration_messages_and_sequences_preserved'] = True
        fixture.report['checks']['original_tls_and_signing_material_preserved'] = all(v['sha256'] == v['source_sha256'] for v in identities.values())
        fixture.verify('restored_takeover', 'advance')
        fixture.report['checks']['new_execution_fence_and_stale_owner_rejection'] = True
        final = json.loads((source / 'target-cold-backup/expected-checkpoint.json').read_text())
        backup = json.loads((cold / 'expected-checkpoint.json').read_text())
        known = {row['command_id'] for row in backup['steps']}
        fixture.report['backup_cutoff'] = {'execution_id': backup['downlink_id'], 'backup_fence': backup['fence'],
                                         'last_target_fence': final['fence'], 'backup_command_ids': sorted(known),
                                         'after_backup_command_ids_requiring_state_reconciliation': [row['command_id'] for row in final['steps'] if row['command_id'] not in known],
                                         'recovery_condition': 'Verify post-backup physical effects and recover later checkpoint/command records before rejoining the original execution domain'}
        fixture.report['all_checks_passed'] = all(fixture.report['checks'].values())
    except Exception as error:
        fixture.report['all_checks_passed'] = False
        fixture.report['error'] = str(error)
        raise
    finally:
        fixture.dc('stop', '-t', '30')
        states = {}
        for node in configuration['services']:
            info = fixture.status(node)
            states[node] = info['State']
            fixture.run(['docker', 'logs', project + '-' + node + '-1'], required=False)
        fixture.report['final_container_states'] = states
        fixture.report['checks']['normal_exit_without_oom'] = all(v['ExitCode'] == 0 and not v['OOMKilled'] for v in states.values())
        fixture.dc('down')
        fixture.report['completed_at'] = datetime.now(timezone.utc).isoformat()
        fixture.report['temporary_project_removed'] = True
        after = {name: {**item, 'sha256': sha(Path(item['path'])), 'bytes': Path(item['path']).stat().st_size} for name, item in inputs.items()}
        (work / 'compile-inputs-after.json').write_text(json.dumps(after, indent=2) + '\n')
        fixture.report['compile_inputs_after'] = {'path': str(work / 'compile-inputs-after.json'), 'sha256': sha(work / 'compile-inputs-after.json'), 'count': len(after)}
        fixture.report['checks']['compile_inputs_unchanged'] = after == inputs
        fixture.report['all_checks_passed'] = fixture.report.get('all_checks_passed', False) and all(fixture.report['checks'].values())
        fixture.save()
    print('Verified independent old NATS cold restore: ' + str(work / 'verification.json'))


if __name__ == '__main__':
    main()
