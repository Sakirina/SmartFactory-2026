import type { EChartsOption } from 'echarts';
import type { Alarm, DataResult, Point } from './types';
import { exactTimestamp, stringifyExactJSON } from './precision';

export function exactPointValue(point: Point) { return typeof point.value === 'string' ? point.value : stringifyExactJSON(point.value); }
const escapeTooltip = (value: string) => value.replace(/[&<>"']/g, character => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[character]!));
export function pointCoordinate(point: Point): number | null {
  const value = point.value;
  if (typeof value === 'boolean') return Number(value);
  if (typeof value === 'number' || typeof value === 'bigint') return Number.isFinite(Number(value)) ? Number(value) : null;
  if (value && typeof value === 'object' && !Array.isArray(value) && ['number','bigint'].includes(typeof value.average)) return Number.isFinite(Number(value.average)) ? Number(value.average) : null;
  return null;
}
export function trendOption(data: DataResult | null, dark = false, alarms: Alarm[] = []): EChartsOption {
  const groups = new Map<string, Point[]>(); const booleans = new Set<string>();
  for (const point of data?.points ?? []) {
    const name = `${point.device_id} / ${point.key}${point.unit ? ' (' + point.unit + ')' : ''}`;
    const items = groups.get(name) ?? []; items.push(point); groups.set(name,items);
    if (typeof point.value === 'boolean') booleans.add(name);
  }
  const times = (data?.points ?? []).map(point=>point.observed_ms);
  const subSecond = times.length>1 && Math.max(...times)-Math.min(...times)<1000;
  const text = dark ? '#c6d5e8' : '#63758a'; const mixed = booleans.size > 0 && booleans.size < groups.size;
  const numeric = { type: 'value' as const, name: '数值', scale: true, nameTextStyle: { color: text }, axisLabel: { color: text, formatter: (value: number) => value !== 0 && (Math.abs(value)>=1e9 || Math.abs(value)<0.001) ? value.toExponential(2) : value.toLocaleString('zh-CN',{maximumFractionDigits:3}) }, splitLine: { lineStyle: { color: dark ? '#24364c' : '#ecf0f3' } } };
  const boolean = { type: 'value' as const, name: '状态', min: 0, max: 1, interval: 1, nameTextStyle: { color: text }, axisLabel: { color: text, formatter: (value: number) => value === 1 ? '开启' : '关闭' }, splitLine: { show: false } };
  return {
    color: ['#198b7e','#548dce','#e0a043','#ae75c5','#d77662'], animationDuration: 150, animationDurationUpdate: 150,
    grid: { top: 38, right: mixed ? 24 : 12, bottom: 82, left: 8, containLabel: true },
    tooltip: { trigger: 'axis', confine: true, renderMode: 'html', extraCssText: 'max-width:min(420px,84vw);max-height:260px;overflow:auto;white-space:normal;overflow-wrap:anywhere;', formatter: (parameters: unknown) => '<div class="sf-chart-tooltip">' + (Array.isArray(parameters) ? parameters : [parameters]).map(parameter => {
      const item = parameter as { seriesName: string; data?: { point?: Point } };
      return item.data?.point ? escapeTooltip(`${exactTimestamp(item.data.point.observed_ms)}\n${item.seriesName}\n精确值：${exactPointValue(item.data.point)}${item.data.point.unit ? ' ' + item.data.point.unit : ''}\n质量：${item.data.point.quality} / 来源：${item.data.point.source_id} / 修订：${String(item.data.point.revision)}`).replace(/\n/g,'<br/>') : '';
    }).join('<br/><br/>') + '</div>' },
    legend: { type: 'scroll', bottom: 0, textStyle: { color: text, fontSize: 11 } },
    xAxis: { type: 'time', axisLabel: { color: text, hideOverlap:true, formatter: (value: number) => new Date(value).toLocaleTimeString('zh-CN',{hour12:false,timeZone:'Asia/Shanghai'}) + (subSecond ? '.'+String(new Date(value).getUTCMilliseconds()).padStart(3,'0') : '') }, axisLine: { lineStyle: { color: dark ? '#35465c' : '#dae2e9' } }, splitLine: { show: false } },
    yAxis: mixed ? [numeric,boolean] : booleans.size ? [boolean] : [numeric], dataZoom: [{ type: 'inside' }, { type: 'slider', bottom: 30, height: 16 }],
    series: [...groups].map(([name, points], index) => ({ id: name, name, type: 'line' as const, step: booleans.has(name) ? 'end' as const : false, yAxisIndex: mixed && booleans.has(name) ? 1 : 0, showSymbol: false, connectNulls: false,
      data: points.sort((a,b) => a.observed_ms - b.observed_ms).map(point => ({ value: [point.observed_ms, point.quality === 'BAD' ? null : pointCoordinate(point)], point })), lineStyle: { width: 2 },
      markLine: index === 0 ? { symbol: 'none', silent: false, lineStyle: { color: '#dd9540', type: 'dashed' as const }, label: { formatter: '数据修订', color: '#aa681c', fontSize: 10 }, data: (data?.revisions ?? []).map(revision => ({ xAxis: revision.at_ms, name: revision.id, tooltip: { formatter: () => escapeTooltip(`${exactTimestamp(revision.at_ms)}\n${stringifyExactJSON(revision.before)} → ${stringifyExactJSON(revision.after)}`).replace(/\n/g,'<br/>') } })) } : undefined,
      markPoint: { symbol: 'triangle', symbolSize: 12, itemStyle: { color: '#d77662' }, data: alarms.filter(alarm => points.some(point => point.device_id === alarm.entity_id) && Number.isFinite(Number(alarm.value))).map(alarm => ({ name: alarm.id, coord: [alarm.updated_ms, Number(alarm.value)], value: alarm.severity, tooltip: { formatter: () => escapeTooltip(`${alarm.severity}\n${exactTimestamp(alarm.updated_ms)}\n${stringifyExactJSON(alarm.value)}`).replace(/\n/g,'<br/>') } })) },
    })),
  };
}
