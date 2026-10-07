import assert from 'node:assert/strict';
import { after, test } from 'node:test';
import { mkdtemp, readFile, writeFile, rm } from 'node:fs/promises';
import path from 'node:path';
import { tmpdir } from 'node:os';
import { pathToFileURL, fileURLToPath } from 'node:url';
import ts from 'typescript';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const temporary = await mkdtemp(path.join(tmpdir(), 'smartfactory-phase4-values-'));
for (const name of ['precision','query-window','metadata-values','chart-data','resource-navigation']) {
  const source = (await readFile(path.join(root,'src',name+'.ts'),'utf8')).replaceAll("'./precision'","'./precision.mjs'");
  await writeFile(path.join(temporary,name+'.mjs'),ts.transpileModule(source,{compilerOptions:{target:ts.ScriptTarget.ES2022,module:ts.ModuleKind.ES2022}}).outputText);
}
after(()=>rm(temporary,{force:true,recursive:true}));
const window = await import(pathToFileURL(path.join(temporary,'query-window.mjs')));
const metadata = await import(pathToFileURL(path.join(temporary,'metadata-values.mjs')));
const charts = await import(pathToFileURL(path.join(temporary,'chart-data.mjs')));
const navigation = await import(pathToFileURL(path.join(temporary,'resource-navigation.mjs')));
const precision = await import(pathToFileURL(path.join(temporary,'precision.mjs')));
const row = (id, revision, value, sort_ms=1000) => ({kind:'trend',id,revision,version:1,sort_ms,data:{id,device_id:'设备/一号',key:'count',value,observed_ms:sort_ms,source_id:'edge-a',quality:'GOOD',revision:1}});
test('complete delta applies exact revisions, removals and metadata before advancing opaque cursor',()=>{
  const state = window.snapshotWindow({items:[row('a','9007199254740993',1),row('b','2',2)],snapshot_cursor:'opaque:snapshot',metadata:{sources:[{id:'edge-a',status:'online'}]},has_more:false});
  const event = {type:'delta',changes:[{operation:'upsert',kind:'trend',id:'a',revision:'9007199254740994',item:row('a','9007199254740994',9007199254740993n)},{operation:'remove',kind:'trend',id:'b',revision:'4'}],metadata:{sources:[{id:'edge-a',status:'offline'}]},has_more:true};
  const next = window.applyWindowEvent(state,event,'opaque:committed');
  assert.equal(next.cursor,'opaque:committed'); assert.equal(next.page.items.length,1); assert.equal(next.page.items[0].data.value,9007199254740993n); assert.equal(next.page.metadata.sources[0].status,'offline'); assert.equal(next.page.has_more,true); assert.equal(state.cursor,'opaque:snapshot');
  const stale = window.applyWindowEvent(next,{type:'delta',changes:[{operation:'upsert',kind:'trend',id:'b',revision:'3',item:row('b','3',3)}]},'duplicate');
  assert.equal(stale.page.items.length,1); assert.equal(stale.revisions['trend:b'],'4');
});
test('metadata-only events update source freshness even at the same commit cursor',()=>{
 const state=window.snapshotWindow({items:[],snapshot_cursor:'same',metadata:{sources:[]},has_more:false});
 const next=window.applyWindowEvent(state,{type:'delta',changes:[],metadata:{sources:[{id:'offline-now'}]},has_more:false},'same');
 assert.equal(next.cursor,'same');assert.equal(next.page.metadata.sources[0].id,'offline-now');
});
test('invalid event revision leaves complete previous window intact',()=>{
 const state=window.snapshotWindow({items:[row('a','1',1)],snapshot_cursor:'before'});
 assert.throws(()=>window.applyWindowEvent(state,{type:'delta',changes:[{operation:'remove',kind:'trend',id:'a',revision:'invalid'}]},'after'));
 assert.equal(state.page.items.length,1);assert.equal(state.cursor,'before');
});
test('metadata integer parsing retains numeric identity and validates enums, range and required fields',()=>{
 assert.equal(metadata.parseField({type:'integer',required:true},'9007199254740993'),9007199254740993n);
 assert.throws(()=>metadata.parseField({type:'integer',required:true},'1.5'));
 assert.deepEqual(metadata.metadataIssues([{key:'port',label:'端口',type:'integer',minimum:1,maximum:65535},{key:'mode',label:'模式',type:'string',enum:['on','off']},{key:'host',label:'地址',required:true,type:'string'}],{port:70000,mode:'other'}),['端口超过最大值 65535','模式需要使用目录中的选项','地址为必填参数']);
 const parameters={value:9007199254740993n,extension:{counter:9223372036854775807n}};
 assert.equal(precision.stringifyExactJSON(precision.parseExactJSON(precision.stringifyExactJSON(parameters))),'{"value":9007199254740993,"extension":{"counter":9223372036854775807}}');
});
test('chart approximate coordinates retain precise tooltip values and units',()=>{
 const point=row('large','1',9007199254740993n).data;point.unit='件';
 const option=charts.trendOption({points:[point],revisions:[],quality:{},sources:[]});
 assert.equal(charts.pointCoordinate(point),9007199254740992);assert.equal(charts.exactPointValue(point),'9007199254740993');
 const chartPoint=option.series[0].data[0];assert.equal(chartPoint.point.value,9007199254740993n);
 const tooltip=option.tooltip.formatter([{seriesName:'货物计数',data:chartPoint}]);assert.match(tooltip,/9007199254740993 件/);assert.match(tooltip,/edge-a/);
 assert.equal(option.tooltip.renderMode,'html');
 const hostile={...chartPoint,point:{...point,source_id:'<script>alert(1)</script>'}};assert.doesNotMatch(option.tooltip.formatter([{seriesName:'<img onerror=alert(1)>',data:hostile}]),/<script>|<img onerror/);
});
test('resource navigation escapes complete Chinese slash and colon identifiers',()=>{
 const link=navigation.resourceLink('definition','调查/温度:一号',9007199254740993n);
 assert.equal(new URLSearchParams(link.split('?')[1]).get('definition'),'调查/温度:一号');
 assert.match(link,/%2F/);assert.match(link,/%3A/);assert.match(link,/9007199254740993/);
});


test('a late HTTP window preserves the later committed SSE rows tombstones and opaque cursor',()=>{
  const earlier = { ...window.snapshotWindow({items:[{kind:'trend',id:'old',revision:'1',sort_ms:1,data:{value:81}}],snapshot_cursor:'opaque-http'}), appliedOrder: window.nextWindowOrder() };
  const latest = { ...window.applyWindowEvent(earlier,{type:'delta',changes:[{kind:'trend',id:'old',revision:'2',operation:'remove'},{kind:'trend',id:'new',revision:'9007199254740993',operation:'upsert',item:{kind:'trend',id:'new',revision:'9007199254740993',sort_ms:2,data:{value:9999}}}]},'opaque-sse-latest'), appliedOrder: window.nextWindowOrder() };
  assert.equal(window.preferCommittedWindow(latest,earlier),latest);
  assert.equal(window.preferCommittedWindow(latest,earlier).cursor,'opaque-sse-latest');
  const refreshed = {...latest,appliedOrder:window.nextWindowOrder()};
  assert.equal(window.preferCommittedWindow(latest,refreshed),refreshed);
});
