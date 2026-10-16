#!/usr/bin/env python3
import collections
import datetime
import hashlib
import json
import shutil
from pathlib import Path

ROOT = Path('/path/to/SmartFactory-2026')
BASE = Path('/private/tmp/smartfactory-final-independent-20261007')
SUMMARY = BASE / 'final-summary'
FIXED = SUMMARY / 'fixed-final-records'
FIXED.mkdir(exist_ok=True)
CACHE = {}


def sha(path):
    p = Path(path)
    key = (str(p), p.stat().st_size, p.stat().st_mtime_ns)
    if key not in CACHE:
        with p.open('rb') as f:
            CACHE[key] = hashlib.file_digest(f, 'sha256').hexdigest()
    return CACHE[key]


def path(value):
    p = Path(value)
    return p if p.is_absolute() else ROOT / p


def ref(value):
    p = path(value)
    return {'path': str(p), 'sha256': sha(p), 'bytes': p.stat().st_size}


def read(value):
    return json.loads(path(value).read_text())


def fixed(value):
    p = path(value)
    target = FIXED / (sha(p) + p.suffix)
    if not target.exists():
        shutil.copyfile(p, target)
    assert sha(target) == sha(p)
    return {'original_path': str(p), **ref(target)}


def b(value):
    return str(BASE / value)


def evidence(values):
    return [ref(v) for v in values]


now = datetime.datetime.now(datetime.timezone.utc).isoformat()
acceptance = read('docs/evolution/acceptance.json')
matrix = read('.local/evolution/final-review/execution-matrix.json')
delivery = read('.local/evolution/final-delivery-verified.json')
assert delivery.get('status') in ('passed', 'complete', 'verified'), delivery.get('status')
coordination = {name: fixed(name) for name in [
    'docs/evolution/acceptance.json',
    '.local/evolution/phase5-coordinator-findings.json',
    '.local/evolution/phase5-acceptance-watchpoints.json',
]}
for name in ['docs/evolution/team.json', 'docs/evolution/status.md']:
    if path(name).is_file():
        coordination[name] = fixed(name)

groups = {
    'platform_full': {
        'status': 'passed', 'counts_including_subtests': {'pass': 589, 'skip': 93, 'fail': 0},
        'result': '最终 Platform 全模块普通回归通过，5536 项保留输入前后相同；93 条条件跳过保留原状态并逐项提供后续对应',
        'evidence': evidence([b('platform-ordinary/verification.json'), b('platform-ordinary/conditional-coverage-completed.json')]),
    },
    'datatransfer_full': {
        'status': 'passed_with_explicit_exact_input_reuse', 'counts_including_subtests': {'pass': 109, 'skip': 7, 'fail': 0},
        'result': '596 包选择、4268 个实际选择路径及全部4318项记录输入与已通过完整回归一致；七条条件跳过沿用原外部专项与辅助入口的解释',
        'evidence': evidence(['/private/tmp/smartfactory-delivery-regressions-current-20261005/verification.json', b('go-selection/datatransfer.metadata.json'), b('readonly/last-full-regression-current-input-comparison.json'), '.local/evolution/phase5-delivery-current-regressions-root-review.json']),
    },
    'pg_followup': {
        'status': 'passed', 'counts_including_subtests': {'pass': 91, 'skip': 0, 'fail': 0},
        'result': '实际 PostgreSQL18、DataTransfer 配置消费者与 HR 进程的受影响条件用例通过；完整93条对应包括当前执行78条、原外部环境7条、当前辅助夹具1条、由父测试执行的辅助入口2条、相同编译图3条及相同N04输入2条',
        'cleanup_observation': '首次结束时观察到一条尚未退出的本轮连接；后续独立查询为零，隔离schema为空，本轮数据库已删除。首次结果及补充清理记录分别保留',
        'evidence': evidence([b('platform-conditional/verification.json'), b('platform-conditional/cleanup-followup.json'), b('platform-ordinary/conditional-coverage-completed.json'), b('readonly/node-n04-race-inputs-current.json')]),
    },
    'bounded_race': {
        'status': 'passed', 'current_named_test_events': 19,
        'result': '当前发布初始化、不同规则发布往返、缓存授权、旧程序失败后的新目标恢复和同代等待完成有限race；5486项输入前后相同。N04既有19场景与D01既有10场景分别按其相同受测输入适用',
        'evidence': evidence([b('platform-race/verification.json'), b('readonly/node-n04-race-inputs-current.json'), '.local/evolution/node-connector-deadlock-verified.json', '.local/evolution/datatransfer-runtime-followup-verified.json']),
    },
    'frontend_build': {
        'status': 'passed_with_verified_fixed_final_build', 'retained_inputs': 30931, 'source_inputs': 295, 'production_assets': 19,
        'result': '最终固定构建的生成客户端、TS7、TS6、27项单测及生产产物均与当前输入对应；295项前端相关输入中291项为公开包源码，4项内部或系统输入按构建记录说明',
        'evidence': evidence([b('readonly/frontend-fixed-input-audit.json'), '.local/evolution/release-interface-verified.json', b('frontend/snapshot.json'), b('package-audit/verification-final.json')]),
    },
    'business_browser': {
        'status': 'passed', 'current_business_query_precision_ai_cases': 27,
        'result': '同一最终19份生产产物完成设备表单、草稿编辑与语义检查、告警、工单交接、五模板、查询订阅、精确值和AI依据；原控制、历史、任务与连续模拟页面保留已验收原记录，并核对16份未变功能及传输源码与166条当前相关后端通过事件',
        'evidence': evidence([b('final-summary/browser-summary.json'), b('readonly/legacy-browser-current-correspondence.json'), b('final-summary/browser-artifact-index.json')]),
    },
    'release_browser': {
        'status': 'passed', 'current_phase5_cases': 12,
        'result': '空目录首份发布、固定组件组合、同页URL切换、发布与身份及模板的延迟刷新保留输入、只读和部分编辑权限、双节点分批发布、动态和静态配置以及撤权轮换全部完成实际页面验证',
        'evidence': evidence([b('final-summary/browser-summary.json'), b('phase5-browser/browser-parameterization.json'), b('phase5-browser/api-a01-independent.json')]),
    },
    'precision_browser': {
        'status': 'passed',
        'result': '当前三个精确值/导出/订阅用例通过；云端、现场、大屏三种尺寸共九个画面使用同一最终固定产物的专项记录，并已核对其全部输入和产物',
        'evidence': evidence([b('browser-results/browser-precision-first/browser-results.json'), '/private/tmp/smartfactory-release-interface-20261005/browser-surfaces-final-second/all-nine-surfaces.state.json', b('readonly/frontend-fixed-input-audit.json')]),
    },
    'release_two_node': {
        'status': 'passed', 'behavior_checks': 59, 'private_value_absence_checks': 122,
        'result': '独立 cloud/config/两个edge、各自SQLite数据库、两个代理和实际配置消费者完成181条记录；页面又在自己的两个实际目标上验证离线等待、批次阻断、恢复、业务版本冲突与15秒运行报告过期',
        'node_fixture_counts': '节点初始夹具实际保存14条通过断言和12条Schema检查；原结果内存计数15与持久记录14的差异已独立说明，后续页面验证直接核对实际消费者',
        'evidence': evidence([b('release-runtime/result.json'), b('release-runtime/evidence/assertions.json'), b('node-runtime-v2/result.json'), b('runtime-count-audit.json'), b('phase5-browser/runs/pipeline-first/results.json'), b('final-summary/executed-input-and-binary-audit-compact.json')]),
    },
    'linux_packaged_entry': {
        'status': 'passed', 'checks': 55, 'commands': 202,
        'result': '正式Linux amd64包在独立PG业务库、一个代理管理edge和正式DataTransfer程序中完成准备、首次转换、两版本实际消费者、规则往返、断开cloud/config后的缓存启动、停止和保留数据再次启动；八项服务最终退出0且无OOM，本轮三个PG业务库连接归零',
        'scope': 'Linux正式包运行采用一个管理节点；双节点批次与失败恢复由本审查独立双节点组合覆盖',
        'evidence': evidence([b('package-audit/runtime-evidence-audit.json'), '/private/tmp/smartfactory-delivery-final-runtime-20261007/verification.json', '.local/evolution/phase5-final-package-root-review.json']),
    },
    'package_public': {
        'status': 'passed', 'manifest_files': 1533, 'archived_files_including_manifest': 1534, 'current_public_sources': 894, 'linux_programs': 14, 'frontend_assets': 19,
        'result': '十四程序、迁移、契约、工具、文档、依赖和许可完整对应；十个容器组件均有锁定来源及许可引用。归档逐文件与实际受测包一致，包摘要与校验文件一致',
        'evidence': evidence([b('package-audit/verification-final.json'), b('package-audit/archive-audit.json'), b('package-audit/runtime-evidence-audit.json'), '.local/evolution/final-delivery-verified.json']),
    },
    'final_criterion_audit': {
        'status': 'independent_review_passed',
        'result': '45条条件分别对应最终源码、实际输入、当前执行或有依据的历史结果；14个第五阶段问题和14项关注记录分别给出结论，主协调据此更新最终验收状态',
        'evidence': evidence([b('readonly/requirement-inventory.json'), b('final-summary/executed-input-and-binary-audit-compact.json'), b('package-audit/archive-audit.json'), b('cleanup/shutdown.json')]),
    },
}

conclusions = {
    'E1-01': 'Go1.27.1及Node24.21.0的实际工具记录与版本入口一致，两个Go模块适用完整回归、当前原生程序和Linux十四程序构建形成对应',
    'E1-02': '原生平台、Edge、Envoy及锁定镜像升级的实际版本和通信记录保留，当前包核对镜像摘要、依赖来源与许可，正式Linux入口和原生停止再启动专项覆盖当前集成',
    'E1-03': '兼容清单、正式契约和生成内容一致，允许组合与摘要、格式及版本拒绝通过当前普通回归和实际发布准备检查',
    'E1-04': '统一草稿应用的权限、对象版本、依赖变动和事务回滚由当前应用及HTTP回归验证，页面实际编辑与版本刷新保持输入',
    'E1-05': '实时路由生成契约、旧接口及类型客户端通过检查，实际HTTP和页面执行对应当前契约；A01两类422保留字段位置与消息并省略凭据值',
    'E1-06': 'SQLite与实际PG编号迁移、历史文档修订和重复启动通过，正式包迁移版本11与程序声明及保留数据再次启动一致',
    'E1-07': '追踪、上下文传播和指标聚合的当前命名回归通过，实际业务发布及审计、outbox仍与已验收调用入口对应',
    'E1-08': '生成客户端、精确整数、错误对象与生产类型检查通过，当前云端、现场和大屏实际显示、提示及完整导出保留数值表示',
    'E1-09': '最初完整交付流程保留其受测记录，最终组合另验证当前发布、审计、版本和迁移声明以及正式包入口',
    'E2-01': '规定规模PG16到18迁移和原生兼容保留实际历史记录；当前PG18持久实例执行独立schema回归，正式包的隔离数据库初始化、启动及保留数据再次启动逐项对应',
    'E2-02': '当前普通和实际PG回归覆盖不同对象并发、同对象竞争、审计、迟提交可见性及事务原子性；来源登记N04以相同固定输入的PG/race加最终Linux并发组合验证',
    'E2-03': '当前PG归档CAS、迟到数据、分区与事务时长用例通过，精确字段往返与失败回滚仍保留专项结果及当前调用关系',
    'E2-04': 'River同事务入队、恢复、任务取消与重试通过当前普通及PG检查，原任务身份及页面流程与未变功能源码对应',
    'E2-05': '队列预算、受控积压、取消与前台处理由当前平台回归及相同输入DataTransfer完整回归验证，原并发资源专项保留其有限负载条件',
    'E2-06': '发布计划序列化、加载复用和兼容拒绝由当前回归及实际发布验证，compiledplan实际PG用例按完全相同编译图沿用',
    'E2-07': '数值、质量、单位、顺序、状态和版本的确定结果通过当前回归，实际发布平均值、最大值及正式返回旧规则分别产生所记录结果和新激活版本',
    'E2-08': '内建节点元数据、后端参数和生成Schema一致，当前页面对范围、枚举、单位和精确扩展字段验证及保存通过',
    'E2-09': '连续模拟与任务详情功能源码及传输层与已验收版本对应，当前状态计算、持久任务、取消重试与PG执行结果补充验证共享后端',
    'E2-10': '官方MCP客户端及既有协议的发现、读取、草稿、会话、权限和错误测试由当前完整平台回归验证，传输实现及兼容清单对应最终源码',
    'E2-11': 'Chat Completions和Responses的结构化消息、交错分片、工具参数与异常结束通过本地录制或模拟端点回归；当前八条AI页面用例验证证据和终止状态',
    'E2-12': '锁定依赖、Node版本、TS7与TS6、单测和生产构建已固定并核对输入，当前业务浏览器和三入口九画面对应同一最终19份产物',
    'E3-01': '持久控制状态、授权过期、重启、重复请求和审计通过当前普通及实际PG检查，控制状态机源码与既有受控进程专项一致',
    'E3-02': '实际云边控制进程检查通过，原发送故障、未知结果核对、执行权与物理动作次数记录按其保存源码适用，当前控制和日志持久化回归覆盖受影响共享存储',
    'E3-03': '角色、版本、参数及状态前提的人工操作由当前后端和受控云边过程验证，原页面核对及恢复记录与未变页面源码对应',
    'E3-04': '当前跨版本历史、不可变快照、回放隔离、River持久执行与PG查询通过，原历史页面的来源、质量和精确值检查按保存版本适用',
    'E3-05': '变化和无变化样本、数值与质量及来源版本的比较通过当前回归，原完整页面流程与未变历史功能及传输源码对应',
    'E3-06': '当前影子执行、崩溃恢复、实时传播、幂等与正式状态检查通过，原影子页面和任务取消重试记录对应当前未变功能以及本轮共享后端回归',
    'E3-07': '执行、动作、回放和调查的记录关联与授权通过当前回归；原控制历史解释页面和当前AI证据导航覆盖各自实际内容',
    'E4-01': '数据库投影、范围授权、分页修订和重建通过当前普通及PG回归；浏览器实际权限变化后的旧窗口清理和新快照获取通过',
    'E4-02': '迟提交、多个订阅、撤权和游标恢复通过当前PG及普通回归，实际浏览器核对心跳、新提交、opaque游标重连与延迟HTTP不覆盖SSE',
    'E4-03': '页面模块、缓存身份与生成客户端通过最终构建；实际用户切换、撤权、编辑保留、同页发布URL来回及清除验证当前状态组织',
    'E4-04': '三协议表单、节点元数据与精确扩展字段往返通过实际页面及后端检查，节点配置实际消费者验证动态、失败恢复及静态新进程采用',
    'E4-05': '图节点、连接及参数变化的语义检查和编辑保持通过当前业务页面；循环、缺失依赖和资源范围由当前应用回归核对',
    'E4-06': '持续采样时告警人工原因与版本依据保持，状态冲突、审计及处置动作通过当前页面和实际PG并发检查',
    'E4-07': '当前八条AI实际页面流程及三项PG调查覆盖五工具依据、伪造引用、跨调查、撤权、过期、删除、会话切换与异常流',
    'E4-08': '创建、分配、处理、交接及由新负责人完成的实际页面通过，原始操作与当前版本依据、权限和审计通过当前后端并发检查',
    'E4-09': '五类模板固定设备和连接器版本生成25份草稿，冲突零草稿及追加重试记录通过；最终模板依据刷新保留新参数并完成实际演进提交',
    'E4-10': '云端、现场与大屏同一最终构建的三尺寸九画面记录通过，当前三条精确值、图表提示及完整导出用例另行通过',
    'E5-01': '独立组合校验四类组件、依赖、摘要和兼容关系，空目录实际创建首份固定清单；正式包程序与源摘要及发布内容一致',
    'E5-02': '独立cloud/config/双edge和实际消费者验证节点范围、身份代际、撤权、轮换、固定版本与应用确认；收到明确授权拒绝后清理缓存，服务临时不可达时保留已核验配置',
    'E5-03': '期望、准备、实际应用和运行摘要来自实际程序；重复心跳保持业务版本，新实例取得当前代际，旧实例和旧分配被拒绝，运行报告15秒过期及恢复按实际进程核对',
    'E5-04': '双节点独立组合完成离线等待、失败阻断、显式重试、兼容退回和中断恢复；同代失败等待及新发布恢复有普通、PG、race和历史原数据证据，正式Linux入口完成保留数据再次启动',
    'E5-05': '当前页面完成空目录组合、固定清单与目标筛选、发布双节点进度、权限、URL切换、身份和模板版本冲突刷新；实际模板演进提交保留新输入',
    'E5-06': '两个Go模块适用回归、有限PG/race、生成契约、最终前端、协议与云边控制专项和正式Linux包均已对应；归档1534个文件与受测包一致，十四程序及依赖来源和许可完整',
    'E5-07': '独立审查逐项确认45条适用结果，14个问题完成修正后验证、14项关注条件有定位证据；自建运行资源已清理，整体状态由主协调完成最终登记',
}
assert set(conclusions) == {r['id'] for r in acceptance['requirements']}
matrix_by_id = {r['id']: r for r in matrix['requirements']}
mutable_names = set(coordination)
requirements = []
for item in acceptance['requirements']:
    m = matrix_by_id[item['id']]
    historical = []
    for value in m['historic_evidence']:
        if value in mutable_names:
            historical.append(coordination[value])
        else:
            historical.append(ref(value))
    implementation = []
    for value in item.get('implementation', []):
        p = path(value)
        if p.is_file():
            implementation.append(fixed(value))
        elif p.is_dir():
            implementation.extend(fixed(f) for f in sorted(p.rglob('*')) if f.is_file() and '.local' not in f.parts)
        else:
            raise AssertionError(('missing implementation', value))
    requirements.append({
        'id': item['id'], 'phase': item['phase'], 'title': item['title'],
        'requirement': item['requirement'], 'verification_criteria': item['verification_criteria'],
        'final_independent_status': 'passed', 'conclusion': conclusions[item['id']],
        'verification_groups': m['verification_groups'],
        'current_implementation_at_review': implementation,
        'prior_evidence_with_recorded_scope': historical,
        'current_evidence': [r for key in m['verification_groups'] for r in groups[key]['evidence']],
        'source_correspondence': evidence([b('readonly/phase4-current-source-comparison-revised.json'), b('final-summary/executed-input-and-binary-audit-compact.json'), b('package-audit/verification-final.json')]),
    })

finding_sources = read('.local/evolution/phase5-coordinator-findings.json')['items']
findings_plan = {
    'P5-N01': ('当前完整目标、身份代际和实例代际共同限制密钥解析，固定目标移除及空目标保持的权限场景通过当前工作负载回归和历史相同修正专项', ['platform_full', 'pg_followup', 'release_two_node']),
    'P5-N02': ('当前工作负载重连撤销回归与实际页面停用轮换通过；服务临时不可达时保留缓存，收到401/403明确授权拒绝后恢复默认策略并清理旧连接器，新实例重新采用授权内容', ['platform_full', 'release_browser', 'release_two_node']),
    'P5-N03': ('所属服务不可达返回503；正式prepare-only在原Edge在线时准备，之后沿用数据库和主密钥由代理启动，当前分配变化重新核验，实际Linux包入口通过', ['release_two_node', 'linux_packaged_entry']),
    'P5-A01': ('独立HTTP核对缺expected_version及短凭据两种422，均有details.message/location且无value及凭据回显，身份版本和凭据代次保持原值', ['platform_full', 'release_browser']),
    'P5-F01': ('当前最终产物的独立空目录场景从正式程序、规则、参数与连接器创建首份发布，校验、登记及读取通过', ['release_browser']),
    'P5-F02': ('实际延迟HTTP期间输入的发布原因、身份字段及模板参数在刷新后保留，当前版本依据核对后实际提交通过', ['release_browser']),
    'P5-F03': ('只读身份保留已有发布详情，部分编辑身份组合获授权组件，均无无权配置管理目录导致的详情清空', ['release_browser']),
    'P5-R01': ('本地文档修订与固定参数版本分别保留，当前SQLite/PG初始化和缓存恢复回归通过；原数据及主密钥恢复保留历史备份，最终Linux再次启动通过', ['platform_full', 'pg_followup', 'bounded_race', 'linux_packaged_entry']),
    'P5-D01': ('相同受控订阅停止场景保留旧失败与修正后成功，当前DataTransfer完整输入一致；正式Linux完整组合的gateway退出0，未完成业务工作的超时检查仍保留', ['datatransfer_full', 'bounded_race', 'linux_packaged_entry']),
    'P5-R02': ('当前不同规则集合A/B/A/A往返和外部修改拒绝通过普通、实际PG及race，原25规则模板发布返回普通发布的恢复证据保留；最终Linux规则往返和退回通过', ['platform_full', 'pg_followup', 'bounded_race', 'linux_packaged_entry']),
    'P5-N04': ('旧PG死锁时点、来源锁及失败请求保留，最终排序加锁与快照读取按相同4855项race输入核对，19场景PG/race与最终Linux来源登记和两版本采用通过', ['pg_followup', 'bounded_race', 'linux_packaged_entry']),
    'P5-F04': ('同一页面切换其他批次、返回和清除URL参数时，当前详情及请求跟随所选对象，旧操作依据的输入按对象条件处理', ['release_browser']),
    'P5-B01': ('正式停止及备份命令显式启用release profile；原失败和修正后八次Compose解析保留，最终包脚本及Linux首次转换、停止再启动通过', ['package_public', 'linux_packaged_entry']),
    'P5-B02': ('最终包十个容器组件都有来源及许可材料，284条许可文件引用存在；新增来源材料、最终构建、实际Linux运行和归档内容一致', ['package_public', 'linux_packaged_entry']),
}
assert {x['id'] for x in finding_sources} == set(findings_plan)
findings = []
for f in finding_sources:
    conclusion, group_names = findings_plan[f['id']]
    findings.append({'id': f['id'], 'requirements': f.get('requirements', []),
                     'final_independent_status': 'passed', 'original_observation': f['finding'],
                     'conclusion': conclusion,
                     'original_and_corrected_evidence': [ref(p) for p in f.get('evidence', [])],
                     'current_evidence': [r for g in group_names for r in groups[g]['evidence']]})

watchpoint_groups = {
    'P5-I01': ['release_two_node', 'release_browser', 'linux_packaged_entry'],
    'P5-I02': ['platform_full', 'release_two_node'],
    'P5-I03': ['release_two_node', 'release_browser'],
    'P5-I04': ['release_two_node', 'release_browser', 'linux_packaged_entry'],
    'P5-I05': ['linux_packaged_entry'],
    'P5-I06': ['release_two_node', 'bounded_race'],
    'P5-I07': ['release_two_node', 'bounded_race'],
    'P5-I08': ['release_two_node', 'linux_packaged_entry'],
    'P5-I09': ['release_two_node', 'pg_followup', 'linux_packaged_entry'],
    'P5-I10': ['linux_packaged_entry'],
    'P5-I11': ['release_two_node', 'release_browser'],
    'P5-I12': ['release_browser', 'linux_packaged_entry'],
    'P5-I13': ['datatransfer_full', 'release_browser', 'linux_packaged_entry'],
    'P5-I14': ['bounded_race', 'release_two_node', 'linux_packaged_entry'],
}
watchpoints = []
for w in read('.local/evolution/phase5-acceptance-watchpoints.json')['watchpoints']:
    watchpoints.append({'id': w['id'], 'subject': w['subject'], 'requirements': w['requirements'],
                        'decision': w['decision'], 'verification': w['verification'],
                        'final_independent_status': 'passed',
                        'current_evidence': [r for g in watchpoint_groups[w['id']] for r in groups[g]['evidence']]})

browser = read(b('final-summary/browser-summary.json'))
cleanup = read(b('cleanup/shutdown.json'))
assert len(requirements) == 45 and len(findings) == 14 and len(watchpoints) == 14
assert browser['unique_successful_test_titles'] == 39
assert cleanup['status'] == 'passed' and not cleanup['remaining_live_services'] and not cleanup['remaining_listeners']
assert read(b('package-audit/archive-audit.json'))['status'] == 'passed'
assert read(b('package-audit/verification-final.json'))['status'] == 'passed'
assert read(b('package-audit/runtime-evidence-audit.json'))['status'] == 'passed'

report = {
    'schema_version': 1,
    'status': 'passed', 'completed_at': now,
    'role': 'independent_acceptance_engineer',
    'acceptance_scope': '最终45条计划条件及跨阶段组合；主协调依据本报告更新整体状态',
    'all_45_independent_criteria_passed': True,
    'criterion_counts': {'passed': 45, 'failed': 0, 'pending': 0},
    'phase5_findings': {'verified': 14, 'unresolved': 0},
    'phase5_watchpoints': {'verified': 14, 'pending': 0},
    'recommendation': '45条条件的独立审查通过，建议主协调完成验收状态和交付记录登记后将本轮整体目标标记为完成',
    'excluded_from_this_goal': acceptance['excluded_from_this_goal'],
    'source_sha256': '6c66570a31e7f921e4830fa2a0c16c6947a589c0d4cb0c57aab5fd8d4ba1fdbe',
    'coordination_records_at_review': coordination,
    'delivery_report': ref('.local/evolution/final-delivery-verified.json'),
    'review_execution_matrix': ref('.local/evolution/final-review/execution-matrix.json'),
    'verification_groups': groups,
    'requirements': requirements,
    'findings': findings,
    'watchpoints': watchpoints,
    'input_and_artifact_indexes': evidence([b('final-summary/executed-input-and-binary-audit-compact.json'), b('final-summary/browser-artifact-index.json'), b('package-audit/runtime-evidence-audit.json'), b('package-audit/archive-audit.json')]),
    'retained_nonproduct_observations': [
        {'observation': '浏览器首次Chrome启动、错误的独立夹具路径及单目标详情断言失败', 'resolution': '原结果和参数化前后脚本保留，同条件或定向补验通过', 'evidence': ref(b('final-summary/browser-summary.json'))},
        {'observation': '节点初始断言内存计数15，持久文件14', 'resolution': '按持久14条及独立12条Schema计数，后续页面直接核对实际消费者', 'evidence': ref(b('runtime-count-audit.json'))},
        {'observation': 'PG补充回归结束时一条本轮连接短暂仍在', 'resolution': '随后连接归零、schema为空且删除本轮数据库，原记录未改写', 'evidence': ref(b('platform-conditional/cleanup-followup.json'))},
        {'observation': '公开文件扫描将通用/private/tmp构建缓存和历史测试示例路径列为待审查', 'resolution': '28份命中文件逐条检查，个人目录及身份路径为零，原扫描及解释分别保留', 'evidence': ref(b('package-audit/path-scan-review.json'))},
        {'observation': '节点独立驱动最初在服务启动前遗漏go-list目标，模板前置HTTP验证首次把details假定为errors', 'resolution': '前者原失败文件保留并修正独立驱动，后者改为核对实际details位置，服务及API最终结果通过', 'evidence': evidence([b('node-runtime/failure.json'), b('phase5-browser/api-a01-independent.json')])},
    ],
    'historic_evidence_policy': '历史报告、失败记录和归档继续对应原时点。完全相同输入的执行标记为复用；功能源码未变但共享调用有变化时，列明当前受影响普通、PG、race、浏览器或包入口的补充结果',
    'cleanup': {'status': 'passed', 'selected_processes': 38, 'remaining_owned_services': 0,
                'remaining_selected_listeners': 0,
                'postgres_final_exit_code': 0, 'postgres_oom_killed': False,
                'business_connections_before_postgres_stop': 0,
                'preserved_stopped_containers': 12, 'preserved_volumes': 52,
                'preserved_non_template_databases': 45,
                'evidence': evidence([b('cleanup/shutdown.json'), b('platform-conditional/cleanup-followup.json'), b('platform-race/verification.json'), '/private/tmp/smartfactory-delivery-final-20261007/final-postgres-stop/verification.json', '.local/evolution/phase5-final-resource-root-review.json']),
                'retained_state': '本轮与原暂停服务的数据库、密钥、原始日志、输入副本及失败资料保留；最后验证PG在业务连接归零后正常停止，12个容器、52个卷和45个非模板数据库保留原身份与存储'},
    'report_driver': ref(Path(__file__)),
}
destination = ROOT / '.local/evolution/final-independent-review.json'
destination.write_text(json.dumps(report, ensure_ascii=False, indent=2) + '\n')
fixed_report = SUMMARY / 'final-independent-review.json'
shutil.copyfile(destination, fixed_report)
print(json.dumps({'report': ref(destination), 'fixed_report': ref(fixed_report),
                  'status': report['status'], 'requirements': len(requirements),
                  'findings': len(findings), 'watchpoints': len(watchpoints)}, ensure_ascii=False, indent=2))
