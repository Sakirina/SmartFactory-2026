import { parseExactJSON } from './precision';
import type { Contract } from './generated-client';
export type FieldMetadata = Contract<'ParameterMetadata'> & {sensitive?:boolean};
export type Parameters = Record<string,unknown>;
export function parseField(field: FieldMetadata, text: string): unknown {
  if (!text && !field.required) return undefined;
  if (field.type === 'string') return text;
  const value = parseExactJSON(text);
  if (field.type === 'integer' && !((typeof value === 'number' && Number.isSafeInteger(value)) || typeof value === 'bigint')) throw new Error('请输入完整整数');
  if (field.type === 'number' && typeof value !== 'number' && typeof value !== 'bigint') throw new Error('请输入有效数值');
  if (field.type === 'boolean' && typeof value !== 'boolean') throw new Error('请选择布尔值');
  return value;
}
export function metadataIssues(fields: FieldMetadata[], value: Parameters): string[] {
  const issues: string[] = [];
  for (const field of fields) {
    const key = field.key ?? ''; const item = value[key]; const label = field.label || key;
    if (item === undefined || item === null || item === '') { if (field.required) issues.push(label + '为必填参数'); continue; }
    if (field.enum?.length && !field.enum.includes(String(item))) issues.push(label + '需要使用目录中的选项');
    if (field.type === 'string' && typeof item !== 'string') issues.push(label + '需要文本');
    if (field.type === 'boolean' && typeof item !== 'boolean') issues.push(label + '需要布尔值');
    if (field.type === 'integer' && !((typeof item === 'number' && Number.isSafeInteger(item)) || typeof item === 'bigint')) issues.push(label + '需要完整整数');
    if (field.type === 'number' || field.type === 'integer') {
      if (typeof item !== 'number' && typeof item !== 'bigint') issues.push(label + '需要数值');
      else {
        if (field.minimum !== undefined && item < (typeof field.minimum === 'string' ? parseExactJSON(field.minimum) as number | bigint : field.minimum)) issues.push(label + '低于最小值 ' + String(field.minimum));
        if (field.maximum !== undefined && item > (typeof field.maximum === 'string' ? parseExactJSON(field.maximum) as number | bigint : field.maximum)) issues.push(label + '超过最大值 ' + String(field.maximum));
      }
    }
  }
  return issues;
}
