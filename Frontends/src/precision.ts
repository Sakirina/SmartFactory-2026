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
