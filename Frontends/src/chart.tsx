import { useEffect, useRef } from 'react';
import * as echarts from 'echarts/core';
import { LineChart } from 'echarts/charts';
import { GridComponent, LegendComponent, TooltipComponent, DataZoomComponent, MarkLineComponent, MarkPointComponent } from 'echarts/components';
import { CanvasRenderer } from 'echarts/renderers';
import type { Alarm, DataResult, Revision } from './types';
import { trendOption } from './chart-data';

echarts.use([LineChart,GridComponent,LegendComponent,TooltipComponent,DataZoomComponent,MarkLineComponent,MarkPointComponent,CanvasRenderer]);
echarts.registerTheme('smartfactory-light', { backgroundColor: 'transparent', textStyle: { fontFamily: 'PingFang SC, Microsoft YaHei, sans-serif' } });
echarts.registerTheme('smartfactory-dark', { backgroundColor: 'transparent', textStyle: { color: '#c6d5e8', fontFamily: 'PingFang SC, Microsoft YaHei, sans-serif' }, legend: { textStyle: { color: '#c6d5e8' } } });
export function Trend({ data, onRevision, dark = false, height = 340, alarms = [] }: { data: DataResult | null; onRevision?: (revision: Revision) => void; dark?: boolean; height?: number; alarms?: Alarm[] }) {
  const element = useRef<HTMLDivElement>(null); const instance = useRef<echarts.EChartsType | null>(null); const seriesIdentity = useRef(''); const current = useRef({data,onRevision}); current.current = {data,onRevision};
  useEffect(() => {
    if (!element.current) return;
    const chart = echarts.init(element.current,dark ? 'smartfactory-dark' : 'smartfactory-light',{ renderer: 'canvas' }); instance.current = chart;
    chart.on('click',(parameter: unknown) => { const point = parameter as { componentType: string; name: string }; if (point.componentType === 'markLine') { const revision = current.current.data?.revisions?.find(revision => revision.id === point.name); if (revision) current.current.onRevision?.(revision); } });
    const observer = new ResizeObserver(() => chart.resize()); observer.observe(element.current);
    return () => { observer.disconnect(); chart.dispose(); instance.current = null; };
  },[dark]);
  useEffect(() => { const chart = instance.current; if (!chart) return; const option = trendOption(data,dark,alarms); const identity = JSON.stringify((option.series as {id:string}[]).map(series=>series.id)); if (seriesIdentity.current !== identity) { chart.dispatchAction({type:'hideTip'}); seriesIdentity.current = identity; } chart.setOption(option,{replaceMerge:['series','yAxis']}); },[data,dark,alarms]);
  return <><div ref={element} style={{height,minWidth:0}} role="img" aria-label="测点历史趋势，虚线表示修订，三角标记表示告警" /><small className="chart-precision-note">坐标采用显示近似值，工具提示与数据表保留精确原值；时间使用 Asia/Shanghai。</small></>;
}
