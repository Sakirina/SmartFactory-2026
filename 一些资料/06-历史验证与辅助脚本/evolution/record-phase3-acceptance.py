#!/usr/bin/env python3
from pathlib import Path
import json,datetime
now=datetime.datetime.now(datetime.timezone.utc).isoformat()
def write(p,d):Path(p).write_text(json.dumps(d,ensure_ascii=False,indent=2)+'\n')
r=json.loads(Path('.local/evolution/phase3-independent-review.json').read_text())
assert r['status']=='verified' and r['independent_review_complete'] and not r['unresolved_findings']
p='.local/evolution/phase3-coordinator-findings.json';d=json.loads(Path(p).read_text());d['updated_at']=now
for x in d['items']:
 q=next(v for v in r['findings'] if v['id']==x['id']);x['status']='closed_verified';x['closed_at']=now;x['independent_conclusion']=q['conclusion']
 x['final_verification_evidence']=['.local/evolution/phase3-independent-review.json','.local/evolution/phase3-independent-root-review.json']
write(p,d)
p='docs/evolution/team.json';d=json.loads(Path(p).read_text());d['updated_at']=now;ph=next(x for x in d['phases'] if x['phase']==3)
for a in ph['created']+ph['supporting_existing_roles']:a['status']='handoff_complete'
q=next(a for a in ph['supporting_existing_roles'] if a['name']=='phase2_independent_review')
for e in ('.local/evolution/phase3-independent-review.json','.local/evolution/phase3-independent-root-review.json'):
 if e not in q['evidence']:q['evidence'].append(e)
ph['coordination_note']='第三阶段七项目标通过，十五项问题全部完成修正及独立再验证；所有本阶段角色已结束，自建服务和浏览器已关闭，归档后进入第四阶段'
write(p,d)
p=Path('docs/evolution/status.md');s=p.read_text();s=s.replace('第一阶段九项和第二阶段十二项验收完成，第三阶段后端专业已交付，前端与独立审查正在进行','第一阶段九项、第二阶段十二项及第三阶段七项验收完成，累计28项通过，第四阶段即将开始')
s=s.replace('| 第三阶段进行中 |','| 第三阶段验收完成 |').replace('| 后端报告与实际夹具已交付 |','| 第三阶段验收完成 |').replace('| 后端报告与可启动夹具已交付 |','| 第三阶段验收完成 |')
s=s.replace('并继续使用第二阶段既有前端与独立审查成员，当前同时运行两个','并继续使用第二阶段既有前端、独立审查及存储专业成员，前三阶段角色均已结束')
s+='\n2026-10-05 07:20（Asia/Shanghai）：第三阶段七项目标正式通过，十五项问题全部修正并完成独立再验证。主协调核对最终报告269份摘要引用、347份证据索引和35组命令，其中一组保留T02修复前的预期失败。当前Platform回归214项顶层通过、34项按条件跳过，实际PG七包25项顶层通过，SQLite与PG有限race各17项叶子通过；正常页面在单次读取版本17.260秒后成功取消原失败任务的数据副本。405份源码与4046份编译输入对应，十个自建服务端口及浏览器已关闭。前三阶段累计28项通过，保存第三阶段完整源码后启动第四阶段。\n'
p.write_text(s)
