#!/usr/bin/env python3
"""Complete the preserved skip inventory and record unchanged legacy UI scope."""
import collections
import datetime
import hashlib
import json
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007')

def read(path):
    return json.loads(Path(path).read_text())

def ref(path):
    path = Path(path)
    return {'path': str(path), 'sha256': hashlib.sha256(path.read_bytes()).hexdigest(), 'bytes': path.stat().st_size}

def write(path, obj):
    Path(path).write_text(json.dumps(obj, ensure_ascii=False, indent=2) + '\n')

def events(path):
    result = {}
    for line in Path(path).read_text().splitlines():
        row = json.loads(line)
        if row.get('Test') and row['Action'] in ['pass', 'fail', 'skip']:
            result[(row['Package'], row['Test'])] = row
    return result

ordinary_path = BASE / 'platform-ordinary/platform-tests.stdout.log'
conditional_path = BASE / 'platform-conditional/platform-tests.stdout.log'
ordinary, conditional = events(ordinary_path), events(conditional_path)
original = read(BASE / 'platform-ordinary/conditional-coverage.json')
prior = read(ROOT / '.local/evolution/phase5-delivery-regressions-verified.json')
prior_skips = {row['test']: row for row in prior['modules']['Platform']['skips']}
results = []
for row in original['conditional_cases']:
    item = dict(row)
    key = row['Package'], row['Test']
    if key in conditional and conditional[key]['Action'] == 'pass':
        item.update(final_disposition='current_selected_execution_passed', actual_event=conditional[key], evidence=[ref(conditional_path), ref(BASE / 'platform-conditional/verification.json'), ref(BASE / 'platform-conditional/cleanup-followup.json')])
    elif row['disposition'] == 'reuse_same_input_pass':
        item.update(final_disposition='reuse_exact_compiled_graph_pass', evidence=[ref(BASE / 'platform-ordinary/conditional-coverage.json'), ref('/private/tmp/smartfactory-delivery-regressions-current-20261005/platform-tests.stdout.log')])
    elif row['Package'].endswith('/configcenter') and row['Test'].startswith('TestConnectorVersionRegistrationReaders/'):
        item.update(final_disposition='reuse_exact_n04_postgres_and_race_inputs', evidence=[ref(BASE / 'readonly/node-n04-race-inputs-current.json'), ref(ROOT / '.local/evolution/node-connector-deadlock-verified.json')], scope='Final N04 PG and race each passed 19 named cases; the actual retained input set is identical to current inputs')
    elif row['Test'] == 'TestControlBrowserFixture':
        log = BASE / 'browser-runtime/control/process.log'
        assert '--- PASS: TestControlBrowserFixture' in log.read_text()
        item.update(final_disposition='current_fixture_start_and_normal_stop_passed', evidence=[ref(log), ref(BASE / 'browser-runtime/control/process.json'), ref(BASE / 'native-build/verification.json')], scope='Fresh current compiled fixture initialized actual cloud/Edge plus DataTransfer, established result_unknown, served the frozen static files, and ended on its own stop file; historical control UI interventions retain their separate browser evidence')
    elif row['Test'] in ['TestHistoryCrashProcess', 'TestTaskCrashProcess']:
        parent_names = prior_skips[row['Test']]['parent_tests']
        passed = [event for (package, test), event in {**ordinary, **conditional}.items() if package == row['Package'] and test in parent_names and event['Action'] == 'pass']
        assert len(passed) == len(parent_names), (row['Test'], passed)
        item.update(final_disposition='helper_executed_by_current_parent_tests', parent_events=passed, evidence=[ref(ordinary_path), ref(conditional_path)], scope='The direct helper skip is retained; its current parent tests launch the same test binary and inspect actual kill/recovery behavior')
    else:
        assert row['Test'] in prior_skips, row
        earlier = prior_skips[row['Test']]
        item.update(final_disposition='historical_external_environment_with_current_affected_regression', evidence=earlier['specialized_references'], scope=earlier['explanation'], compilation_note='The current full .test dependency graph may differ through release/configuration integration; this is a scoped historical external execution, with current ordinary, PG and independent service results recorded separately')
    item['counts_as_ordinary_pass'] = False
    results.append(item)
assert len(results) == 93
report = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'completed_correspondence', 'ordinary_skips_remain': 93, 'original_inventory': ref(BASE / 'platform-ordinary/conditional-coverage.json'), 'cases': results, 'summary': dict(collections.Counter(r['final_disposition'] for r in results)), 'ordinary_log': ref(ordinary_path), 'conditional_log': ref(conditional_path), 'reuse_policy': 'Only an identical selected compiled input graph is called exact reuse; other retained external executions are described with their original time and current affected-code regression'}
write(BASE / 'platform-ordinary/conditional-coverage-completed.json', report)

p3 = read(ROOT / '.local/evolution/phase3-preserved-paths.json')['files']
p4 = {r['original'].removeprefix(str(ROOT) + '/'): r for r in read(ROOT / '.local/evolution/phase4-preserved-paths.json')['files']}
core_ui = ['Frontends/src/executions.tsx', 'Frontends/src/control-status.ts', 'Frontends/src/history.tsx', 'Frontends/src/history-input.ts', 'Frontends/src/tasks.tsx', 'Frontends/src/task-status.ts', 'Frontends/src/simulation.tsx', 'Frontends/src/simulation-input.ts', 'Frontends/src/api.ts', 'Frontends/src/transport.ts', 'Frontends/src/generated-client.ts', 'Frontends/src/query.ts', 'Frontends/src/query-cache.ts', 'Frontends/src/query-window.ts', 'Frontends/src/editor-state.ts', 'Frontends/src/precision.ts']
rows = []
for relative in core_ui:
    current = ref(ROOT / relative)
    fourth = p4[relative]
    retained = ref(fourth['preserved'])
    assert retained['sha256'] == fourth['sha256'] == current['sha256'], relative
    third = p3.get(relative)
    rows.append({'relative': relative, 'current': current, 'phase4': retained, 'equal_phase4': True, 'equal_phase3': bool(third and third['sha256'] == current['sha256'])})
changed = read(BASE / 'readonly/phase4-current-source-comparison-revised.json')['changed_existing_source_paths']
changed_feature_code = [p for p in changed if p.startswith(tuple('Platform/internal/' + part + '/' for part in ['control', 'history', 'tasks', 'engine', 'compiledplan', 'native']))]
assert not changed_feature_code
passed_targets = [event for (package, test), event in {**ordinary, **conditional}.items() if event['Action'] == 'pass' and (package.endswith(('/control', '/history', '/tasks', '/engine', '/compiledplan')) or any(text in test for text in ['History', 'Shadow', 'Control', 'Recompute']))]
shared = ['Frontends/src/shell.tsx', 'Frontends/src/style.css', 'Frontends/src/types.ts', 'Frontends/src/generated-routes.ts', 'contracts/1.0/api-types.ts']
ui_report = {'created_at': datetime.datetime.now(datetime.timezone.utc).isoformat(), 'status': 'retained_legacy_browser_scope_applicable_with_current_regression', 'feature_and_transport_sources': rows, 'current_changed_shared_inputs': [ref(ROOT / p) for p in shared], 'shared_input_scope': 'Shared shell, style and generated contracts now include phase-five configuration/release views. Their current build, generated-client/type tests and actual business, query, precision, permission and same-page navigation browser results cover the changed integration. The original control/history/task/simulation browser outcomes retain the original complete bundle and its original backend evidence.', 'historical_browser_reports': [ref(ROOT / '.local/evolution/phase3-independent-review.json'), ref(ROOT / '.local/evolution/phase4-independent-review.json')], 'current_passed_named_events': passed_targets, 'current_backend_logs': [ref(ordinary_path), ref(conditional_path)], 'current_fixed_frontend_inputs': ref(BASE / 'readonly/frontend-fixed-input-audit.json'), 'current_full_source_correspondence': ref(BASE / 'readonly/phase4-current-source-comparison-revised.json'), 'decision': 'Reuse unchanged feature UI flows and the accepted source-preserved historical browser interventions; current affected Store, query, authorization and release startup integration is covered by ordinary and actual PG/control process checks. No repeat of the entire historic UI suite is required.'}
write(BASE / 'readonly/legacy-browser-current-correspondence.json', ui_report)
print(json.dumps({'skips': report['summary'], 'legacy_ui_sources': len(rows), 'current_feature_pass_events': len(passed_targets)}, ensure_ascii=False))
