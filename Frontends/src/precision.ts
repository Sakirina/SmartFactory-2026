// Preserve integers outside JavaScript's exact range for tables and exported JSON.
export function parseAPIJSON(text: string): unknown {
  let normalized = '';
  for (let index = 0; index < text.length;) {
    const start = index;
    if (text[index] === '"') {
      index++;
      while (index < text.length) {
        if (text[index] === '\\') { index += 2; continue; }
        if (text[index++] === '"') break;
      }
      normalized += text.slice(start, index);
    } else if (text[index] === '-' || /[0-9]/.test(text[index])) {
      index++;
      while (index < text.length && /[0-9eE.+-]/.test(text[index])) index++;
      const token = text.slice(start, index);
      normalized += /^-?(?:0|[1-9][0-9]*)$/.test(token) && !Number.isSafeInteger(Number(token)) ? JSON.stringify(token) : token;
    } else {
      normalized += text[index++];
    }
  }
  return JSON.parse(normalized);
}

// Retain the distinction between a JSON integer and a string containing digits.
// A sentinel absent from every input string makes this independent of browser
// support for JSON.parse source contexts or JSON.rawJSON.
export function parseExactJSON(text: string): unknown {
  const tokens: { value: string; string: boolean }[] = [];
  const strings = new Set<string>();
  for (let index = 0; index < text.length;) {
    const start = index;
    if (text[index] === '"') {
      index++;
      while (index < text.length) {
        if (text[index] === '\\') { index += 2; continue; }
        if (text[index++] === '"') break;
      }
      const value = text.slice(start, index);
      strings.add(JSON.parse(value));
      tokens.push({ value, string: true });
    } else if (text[index] === '-' || /[0-9]/.test(text[index])) {
      index++;
      while (index < text.length && /[0-9eE.+-]/.test(text[index])) index++;
      tokens.push({ value: text.slice(start, index), string: false });
    } else tokens.push({ value: text[index++], string: false });
  }
  let prefix = '\u0000smartfactory-integer:';
  while ([...strings].some(value => value.startsWith(prefix))) prefix += ':';
  const integers = new Map<string, bigint>();
  const normalized = tokens.map(token => {
    if (!token.string && /^-?(?:0|[1-9][0-9]*)$/.test(token.value) && !Number.isSafeInteger(Number(token.value))) {
      const marker = prefix + integers.size;
      integers.set(marker, BigInt(token.value));
      return JSON.stringify(marker);
    }
    return token.value;
  }).join('');
  return JSON.parse(normalized, (_key, value: unknown) => typeof value === 'string' && integers.has(value) ? integers.get(value) : value);
}

export function stringifyExactJSON(value: unknown, indentation = 0): string {
  const ancestors = new Set<object>();
  const indent = Math.max(0, Math.min(10, indentation));
  const serialize = (item: unknown, level: number): string | undefined => {
    if (typeof item === 'bigint') return item.toString();
    if (typeof item === 'number' && Number.isInteger(item) && !Number.isSafeInteger(item)) throw new Error('整数已超出 JavaScript 精确范围，请通过精确 JSON 输入');
    if (item === null || typeof item !== 'object') return JSON.stringify(item);
    if (ancestors.has(item)) throw new TypeError('JSON contains a circular reference');
    ancestors.add(item);
    const entries = Array.isArray(item)
      ? item.map(value => serialize(value, level + 1) ?? 'null')
      : Object.entries(item).flatMap(([key, value]) => {
        const result = serialize(value, level + 1);
        return result === undefined ? [] : [JSON.stringify(key) + (indent ? ': ' : ':') + result];
      });
    ancestors.delete(item);
    const [start, end] = Array.isArray(item) ? ['[', ']'] : ['{', '}'];
    return entries.length === 0 ? start + end : indent
      ? start + '\n' + ' '.repeat((level + 1) * indent) + entries.join(',\n' + ' '.repeat((level + 1) * indent)) + '\n' + ' '.repeat(level * indent) + end
      : start + entries.join(',') + end;
  };
  return serialize(value, 0) ?? 'null';
}

export type ExactInteger = number | string | bigint;
export function exactTimestamp(value?: ExactInteger): string {
  if (value === undefined || value === 0 || value === 0n || value === '0') return '暂无记录';
  const numeric = Number(value);
  return Number.isSafeInteger(numeric) && Math.abs(numeric) <= 8640000000000000
    ? new Date(numeric).toLocaleString('zh-CN', { hour12: false, timeZone: 'Asia/Shanghai' })
    : `${String(value)} ms（Unix 毫秒）`;
}
