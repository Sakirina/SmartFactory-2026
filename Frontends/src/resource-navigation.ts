export function resourceLink(kind?: string, id = '', version?: unknown): string {
  const encoded = encodeURIComponent(id);
  if (kind === 'alarm') return '#alarm-workbench?alarm=' + encoded;
  if (kind === 'execution') return '#executions?execution=' + encoded;
  if (kind === 'history_run' || kind === 'analysis_run') return '#history?run=' + encoded;
  if (kind === 'work_order') return '#work-orders?work_order=' + encoded;
  if (kind === 'device' || kind === 'entity') return '#assets?device=' + encoded;
  if (kind === 'query' || kind === 'trend') return '#data?device=' + encoded;
  if (kind === 'draft') return '#analysis?draft=' + encoded;
  if (kind === 'definition' || kind === 'rule') return '#analysis?definition=' + encoded + (version === undefined ? '' : '&version=' + encodeURIComponent(String(version)));
  return '#data';
}
