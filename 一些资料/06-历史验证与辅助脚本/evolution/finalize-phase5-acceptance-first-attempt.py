#!/usr/bin/env python3
"""Finalize coordinator records from the completed specialist reviews."""
from collections import Counter
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import shutil


ROOT = Path(__file__).resolve().parents[2]
LOCAL = ROOT / '.local/evolution'
NOW = datetime.now(timezone.utc).isoformat()


def read(name):
    return json.loads((ROOT / name).read_text())


def write(name, value):
    (ROOT / name).write_text(json.dumps(value, ensure_ascii=False, indent=2) + '\n')


def ref(name):
    path = ROOT / name
    data = path.read_bytes()
    return {'path': str(path), 'sha256': hashlib.sha256(data).hexdigest(), 'bytes': len(data)}


def add_unique(values, additions):
    return list(dict.fromkeys(values + additions))


def main():
    qa_name = '.local/evolution/final-independent-review.json'
    delivery_name = '.local/evolution/final-delivery-verified.json'
    root_name = '.local/evolution/phase5-final-root-review.json'
    qa, delivery = read(qa_name), read(delivery_name)
    acceptance = read('docs/evolution/acceptance.json')
    findings = read('.local/evolution/phase5-coordinator-findings.json')
    watches = read('.local/evolution/phase5-acceptance-watchpoints.json')
    expected = [f'E{phase}-{i:02d}' for phase, count in [(1, 9), (2, 12), (3, 7), (4, 10), (5, 7)] for i in range(1, count + 1)]
    assert qa['status'] == delivery['status'] == 'passed'
    assert qa['all_45_independent_criteria_passed']
    assert qa['criterion_counts'] == {'passed': 45, 'failed': 0, 'pending': 0}
    assert qa['phase5_findings'] == {'verified': 14, 'unresolved': 0}
    assert qa['phase5_watchpoints'] == {'verified': 14, 'pending': 0}
    assert qa['delivery_report']['sha256'] == ref(delivery_name)['sha256']
    assert qa['source_sha256'] == delivery['deliverable']['source_sha256']
    assert acceptance['excluded_from_this_goal'] == qa['excluded_from_this_goal']
    qa_rows = {row['id']: row for row in qa['requirements']}
    qa_findings = {row['id']: row for row in qa['findings']}
    qa_watches = {row['id']: row for row in qa['watchpoints']}
    assert sorted(qa_rows) == sorted(row['id'] for row in acceptance['requirements']) == sorted(expected)
    assert set(qa_findings) == {row['id'] for row in findings['items']}
    assert set(qa_watches) == {row['id'] for row in watches['watchpoints']}
    for row in acceptance['requirements']:
        review = qa_rows[row['id']]
        assert review['final_independent_status'] == 'passed'
        for key in ['id', 'phase', 'title', 'requirement', 'verification_criteria']:
            assert review[key] == row[key], (row['id'], key)
    assert all(row['final_independent_status'] == 'passed' for row in qa_findings.values())
    assert all(row['final_independent_status'] == 'passed' for row in qa_watches.values())
    audit_names = [
        'phase5-final-package-root-review.json', 'phase5-final-archive-root-review.json',
        'phase5-final-resource-root-review.json', 'phase5-final-browser-root-review.json',
        'phase5-final-platform-ordinary-root-review.json', 'phase5-final-pg-root-review.json',
        'phase5-final-race-root-review.json', 'phase5-final-delivery-report-root-review.json',
        'phase5-final-independent-report-root-review.json',
    ]
    audits = {name: read('.local/evolution/' + name) for name in audit_names}
    assert all(value['status'] == 'passed' for value in audits.values())
    assert audits['phase5-final-delivery-report-root-review.json']['report_sha256'] == ref(delivery_name)['sha256']
    assert audits['phase5-final-independent-report-root-review.json']['report_sha256'] == ref(qa_name)['sha256']
    assert audits['phase5-final-package-root-review.json']['source_sha256'] == qa['source_sha256']
    archive = delivery['deliverable']['archive']
    assert ref(archive['path']) == archive
    assert delivery['remaining_work']['delivery_engineering'] == []
    package = audits['phase5-final-package-root-review.json']
    browser = audits['phase5-final-browser-root-review.json']
    resource = audits['phase5-final-resource-root-review.json']
    assert package['runtime_checks_passed'] == 55
    assert browser['unique_cases_passed'] == 39
    assert resource['host_remaining_services'] == []

    backups = LOCAL / 'final-acceptance-before-20261007'
    backups.mkdir(exist_ok=False)
    mutable_names = [
        'docs/evolution/acceptance.json', 'docs/evolution/team.json',
        'docs/evolution/status.md', 'docs/evolution/plan.md', 'docs/evolution/pause-handoff.md',
        '.local/evolution/phase5-coordinator-findings.json',
        '.local/evolution/phase5-acceptance-watchpoints.json',
        '.local/evolution/acceptance-record-audit.json',
    ]
    before = []
    for name in mutable_names:
        source = ROOT / name
        if source.exists():
            target = backups / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copy2(source, target)
            before.append({'original': str(source), **ref(str(target))})
    write(str(backups / 'manifest.json'), {'created_at': NOW, 'files': before})

    root_review = {
        'schema_version': 1, 'checked_at': NOW, 'status': 'passed',
        'role': '主协调者：需求、证据及正式验收登记',
        'source_sha256': qa['source_sha256'],
        'archive': archive,
        'requirement_counts': {'total': 45, 'passed': 45, 'failed': 0, 'pending': 0},
        'phases': [{'phase': phase, 'passed': count} for phase, count in [(1, 9), (2, 12), (3, 7), (4, 10), (5, 7)]],
        'phase5_findings': {'resolved': 14, 'unresolved': 0},
        'phase5_watchpoints': {'verified': 14, 'pending': 0},
        'specialist_reports': {'delivery': ref(delivery_name), 'independent': ref(qa_name)},
        'coordinator_evidence': [ref('.local/evolution/' + name) for name in audit_names],
        'verification_summary': {
            'linux_package_checks': 55, 'independent_browser_cases': 39,
            'linux_programs': 14, 'archive_regular_files': 1534, 'public_source_files': 894,
            'frontend_static_files': 19,
            'platform_ordinary': qa['verification_groups']['platform_full']['counts_including_subtests'],
            'datatransfer_exact_input_reuse': qa['verification_groups']['datatransfer_full']['counts_including_subtests'],
            'current_conditional_specialties': qa['verification_groups']['pg_followup']['counts_including_subtests'],
            'current_bounded_race_events': qa['verification_groups']['bounded_race']['current_named_test_events'],
            'linux_runtime_scope': qa['verification_groups']['linux_packaged_entry']['scope'],
            'historical_result_policy': '完整回归中的条件跳过保留原状态；已保存结果按输入、调用关系和专项覆盖条件逐项采用，具体对应见独立报告',
        },
        'requirements': [{
            'id': row['id'], 'phase': row['phase'], 'title': row['title'],
            'status': 'passed', 'conclusion': row['conclusion'],
            'verification_groups': row['verification_groups'],
        } for row in qa['requirements']],
        'findings': [{'id': row['id'], 'status': 'resolved', 'conclusion': row['conclusion']} for row in qa['findings']],
        'watchpoints': [{'id': row['id'], 'status': 'verified', 'subject': row['subject'], 'verification': row['verification']} for row in qa['watchpoints']],
        'cleanup': {key: resource[key] for key in [
            'containers_preserved_and_stopped', 'volumes_preserved', 'non_template_databases_preserved',
            'business_connections_before_pg_stop', 'postgres_normal_exit', 'postgres_oom',
            'host_service_actions', 'host_remaining_services',
        ]},
        'excluded_from_this_goal': acceptance['excluded_from_this_goal'],
        'remaining_in_scope_work': [],
        'record_finalization': '本报告确认行为与交付证据；正式记录与阶段源码保存的一致性在 acceptance-record-audit.json 和 phase5-completion-audit.json 中另行核对',
    }
    write(root_name, root_review)
    final_refs = [qa_name, root_name]
    acceptance.update({
        'updated_at': NOW, 'completed_at': NOW, 'overall_status': 'complete',
        'final_review': {'status': 'passed', 'checked_at': NOW, 'source_sha256': qa['source_sha256'],
                         'independent': ref(qa_name), 'coordinator': ref(root_name), 'delivery': ref(delivery_name), 'archive': archive},
    })
    for row in acceptance['requirements']:
        review = qa_rows[row['id']]
        row['implementation_status'] = 'complete'
        row['verification_status'] = 'passed'
        row['evidence'] = add_unique(row['evidence'], final_refs)
        row['final_verification'] = {
            'checked_at': NOW, 'status': 'passed', 'report': qa_name,
            'requirement_id': row['id'], 'source_sha256': qa['source_sha256'],
            'conclusion': review['conclusion'],
        }
        if row['phase'] == 5:
            row['notes'].append('2026-10-07最终验收：' + review['conclusion'])
        if row['id'] == 'E5-07':
            row['implementation'] = add_unique(row['implementation'], ['docs/evolution/acceptance.json', 'docs/evolution/status.md', 'docs/evolution/team.json'])
    write('docs/evolution/acceptance.json', acceptance)
    findings.update({'updated_at': NOW, 'all_findings_resolved': True, 'resolved_at': NOW})
    for row in findings['items']:
        row['before_final_status'] = row['status']
        row.update({'status': 'resolved', 'resolved_at': NOW, 'final_conclusion': qa_findings[row['id']]['conclusion']})
        row['final_evidence'] = add_unique(row.get('final_evidence', []), final_refs)
    write('.local/evolution/phase5-coordinator-findings.json', findings)
    watches.update({'updated_at': NOW, 'all_verified': True, 'verified_at': NOW})
    for row in watches['watchpoints']:
        row['before_final_status'] = row['status']
        row.update({'status': 'verified', 'verified_at': NOW})
        row['final_evidence'] = add_unique(row.get('final_evidence', []), final_refs)
    write('.local/evolution/phase5-acceptance-watchpoints.json', watches)

    team = read('docs/evolution/team.json')
    team.update({'updated_at': NOW, 'overall_status': 'complete', 'completed_at': NOW})
    for phase in team['phases']:
        phase['status'] = 'accepted'
        phase['final_confirmation_at'] = NOW
        if phase['phase'] == 5:
            phase['accepted_at'] = NOW
            phase['coordination_note'] = '第五阶段七项及五阶段45项全部正式验收；十四程序交付归档、55项Linux运行、39项独立浏览器及限定规模专项均有对应证据，14个问题和14项关注完成验证。本阶段新增四位，全部本轮任务已结束。'
            for member in phase['created'] + phase.get('supporting_existing_roles', []):
                member['status'] = 'phase_accepted'
                member['accepted_at'] = NOW
                if 'verification_status' in member:
                    member['verification_status'] = 'accepted'
                if member['name'] in ['phase5_final_delivery', 'phase4_independent_review']:
                    member['completed_at'] = NOW
                    member['evidence'] = add_unique(member.get('evidence', []), [delivery_name if member['name'] == 'phase5_final_delivery' else qa_name, root_name])
    write('docs/evolution/team.json', team)

    path = ROOT / 'docs/evolution/status.md'
    text = path.read_text()
    paragraphs = text.split('\n\n')
    paragraphs[1] = '五阶段45项目标已于2026-10-07全部正式验收：最终交付报告、独立逐项结论及主协调复核均通过，第五阶段14个问题和14项验收关注全部完成验证。交付为SmartFactory 1.1.0-dev、Linux amd64，十四个程序与前端、源码、契约、迁移和文档一并归档；同包55项Linux检查及39项独立浏览器检查通过。受测源码摘要为 `6c66570a31e7f921e4830fa2a0c16c6947a589c0d4cb0c57aab5fd8d4ba1fdbe`，各阶段记录见 [验收清单](acceptance.json)。'
    paragraphs.insert(2, '最终产物为 [Linux交付包](../../.local/evolution/delivery/SmartFactory-1.1.0-dev-linux-amd64-20261007-6c66570a31e7.tar.gz)（137,796,066字节），配有 [SHA-256校验文件](../../.local/evolution/delivery/SmartFactory-1.1.0-dev-linux-amd64-20261007-6c66570a31e7.tar.gz.sha256)；归档含1534个普通文件。具体依据见 [交付报告](../../.local/evolution/final-delivery-verified.json)、[独立验收报告](../../.local/evolution/final-independent-review.json) 和 [主协调复核](../../.local/evolution/phase5-final-root-review.json)。容器镜像按包内镜像锁和 [构建说明](../../Wiki/development/build.md) 准备，部署流程见 [安装文档](../../Wiki/installation.md)。')
    paragraphs.insert(3, 'Docker响应已恢复，本轮自建应用、浏览器和验证PostgreSQL均已正常结束，原12个容器及52个卷保留；数据库、备份和密钥材料继续保存在原验证目录。完整UOS安装恢复、单节点和三节点连续断云24小时、受限链路补传补算、实际24小时容量与持续运行测量沿用计划中既定范围。阶段源码及正式记录的保存索引见 `.local/evolution/phase5-accepted-source.json`，暂停经过见 [暂停交接](pause-handoff.md)。')
    text = '\n\n'.join(paragraphs)
    text = text.replace('| 第五阶段继续进行 |', '| 五阶段验收完成 |', 1)
    text = text.replace('| 归档完成，提交总报告与清理记录 |', '| 最终交付及清理记录通过验收 |', 1)
    text = text.replace('| 实际验证完成，汇总逐项结论 |', '| 45项独立结论通过，报告已完成 |', 1)
    text = text.replace('当前构建交付和最终独立验收两位原角色继续工作。', '构建交付和最终独立验收两位原角色已完成最终报告，全部本轮任务结束。', 1)
    local_stamp = datetime.now().astimezone().strftime('%Y-%m-%d %H:%M')
    text += '\n' + local_stamp + '（Asia/Shanghai）：45项正式验收全部通过，五阶段状态统一为完成。交付、独立审查及主协调核对采用同一最终源码摘要和归档摘要，14个问题、14项关注及测试资源处理均完成；正式记录与源码随后保存，最终一致性结果见 `.local/evolution/phase5-completion-audit.json`。\n'
    path.write_text(text)
    path = ROOT / 'docs/evolution/plan.md'
    text = path.read_text()
    title, rest = text.split('\n\n', 1)
    path.write_text(title + '\n\n2026-10-07完成记录：本计划五阶段45项目标已全部通过正式验收，交付包和逐项证据见 [推进记录](status.md) 与 [验收清单](acceptance.json)。以下保留实施范围、阶段次序和原定验收条件。\n\n' + rest)
    path = ROOT / 'docs/evolution/pause-handoff.md'
    text = path.read_text()
    path.write_text('2026-10-07完成记录：恢复后已完成正式Linux十四程序包、全部45项独立审查和正式验收登记，本轮测试服务正常结束；交付与归档索引见 [推进记录](status.md)。以下为原暂停及恢复经过。\n\n' + text)
    print(json.dumps({'status': 'finalized', 'completed_at': NOW, 'requirements': dict(Counter(row['verification_status'] for row in acceptance['requirements'])), 'findings': len(findings['items']), 'watchpoints': len(watches['watchpoints']), 'coordinator_report': ref(root_name), 'before_records': str(backups)}, ensure_ascii=False))


if __name__ == '__main__':
    main()
