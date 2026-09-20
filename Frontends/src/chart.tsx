import { useEffect, useRef } from 'react';
import * as echarts from 'echarts';
import type { DataResult, Revision } from './types';

export function Trend({ data, onRevision, dark = false, height = 340 }: { data: DataResult | null; onRevision?: (r: Revision) => void; dark?: boolean; height?: number }) {
  const element = useRef<HTMLDivElement>(null);
  const instance = useRef<echarts.ECharts | null>(null);
  const clickRevision = useRef({ data, onRevision }); clickRevision.current = { data, onRevision };
  useEffect(() => {
    if (!element.current) return;
    const chart = echarts.init(element.current, undefined, { renderer: 'canvas' }); instance.current = chart;
    chart.on('click', (params: unknown) => { const p = params as { componentType: string; name: string }; if (p.componentType === 'markLine') { const r = clickRevision.current.data?.revisions?.find(r => r.id === p.name); if (r) clickRevision.current.onRevision?.(r); } });
    const observer = new ResizeObserver(() => chart.resize()); observer.observe(element.current);
    return () => { observer.disconnect(); chart.dispose(); instance.current = null; };
  }, []);
  useEffect(() => { const chart = instance.current; if (!chart) return; const groups = new Map<string, [number, number | null][]>(); const booleanGroups = new Set<string>();
    for (const p of data?.points ?? []) { const name = p.device_id + ' / ' + p.key; let value = typeof p.value === 'number' ? p.value : typeof p.value === 'boolean' ? Number(p.value) : null; if (typeof p.value === 'boolean') booleanGroups.add(name); if (p.value && typeof p.value === 'object' && !Array.isArray(p.value) && typeof p.value.average === 'number') value = p.value.average; const series = groups.get(name) ?? []; series.push([p.observed_ms, p.quality === 'BAD' ? null : value]); groups.set(name, series); }
    const revisions = data?.revisions ?? []; const text = dark ? '#c6d5e8' : '#63758a';
    const mixed = booleanGroups.size > 0 && booleanGroups.size < groups.size;
    const numericAxis = { type: 'value' as const, scale: true, axisLabel: { color: text }, splitLine: { lineStyle: { color: dark ? '#24364c' : '#ecf0f3' } } };
    const booleanAxis = { type: 'value' as const, min: 0, max: 1, interval: 1, axisLabel: { color: text, formatter: (value: number) => value === 1 ? '开启' : '关闭' }, splitLine: { show: false } };
    chart.setOption({ color: ['#198b7e', '#548dce', '#e0a043', '#ae75c5', '#d77662'], animationDuration: 180, animationDurationUpdate: 180, grid: { top: 40, right: mixed ? 52 : 24, bottom: 62, left: 58 }, tooltip: { trigger: 'axis', confine: true, renderMode: 'richText' }, legend: { type: 'scroll', bottom: 0, textStyle: { color: text, fontSize: 11 } }, xAxis: { type: 'time', axisLabel: { color: text }, axisLine: { lineStyle: { color: dark ? '#35465c' : '#dae2e9' } }, splitLine: { show: false } }, yAxis: mixed ? [numericAxis, booleanAxis] : booleanGroups.size ? [booleanAxis] : [numericAxis], dataZoom: [{ type: 'inside' }], series: [...groups].map(([name, points], index) => ({ id: name, name, type: 'line', step: booleanGroups.has(name) ? 'end' : false, yAxisIndex: mixed && booleanGroups.has(name) ? 1 : 0, showSymbol: false, connectNulls: false, data: points.sort((a, b) => a[0] - b[0]), lineStyle: { width: 2 }, markLine: index === 0 ? { symbol: 'none', silent: false, lineStyle: { color: '#dd9540', type: 'dashed' }, label: { formatter: '数据修订', color: '#aa681c', fontSize: 10 }, data: revisions.map(r => ({ xAxis: r.at_ms, name: r.id, tooltip: { formatter: () => `${new Date(r.at_ms).toLocaleString()}\n${r.before} → ${r.after}` } })) } : undefined })) }, { replaceMerge: ['series', 'yAxis'] });
  }, [data, dark]);
  return <div ref={element} style={{ height, minWidth: 0 }} role="img" aria-label="测点历史趋势，虚线表示数据修订" />;
}
