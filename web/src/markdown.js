// What the rich editor can open without losing anything. Markdown outside this set (raw HTML,
// footnotes, reference-style links) opens in the plain-text editor instead.
const FENCE = /^(```|~~~)[^\n]*\n[\s\S]*?^\1[ \t]*$/gm
const INLINE_CODE = /`[^`\n]*`/g

export function richSupported(text) {
  const prose = text.replace(FENCE, '').replace(INLINE_CODE, '') // code may contain anything
  return !(
    /^[ \t]{0,3}<\/?[a-zA-Z][^>]*>/m.test(prose) || // HTML block
    /<\/?(?:span|div|br|sup|sub|u|kbd|details|summary|img|a)\b[^>]*>/i.test(prose) || // inline HTML
    /\[\^[^\]]+\]/.test(prose) || // footnote
    /^[ \t]{0,3}\[[^\]]+\]:\s/m.test(prose) // reference link definition
  )
}

// Front matter (--- YAML --- at the very top) is kept aside and written back unchanged.
export function splitFrontMatter(text) {
  const m = /^---\r?\n[\s\S]*?\r?\n---[ \t]*(?:\r?\n|$)/.exec(text)
  return m ? [m[0], text.slice(m[0].length)] : ['', text]
}

// The editor writes extra blank lines around some blocks (tables); keep at most one, except
// inside code blocks, and end with one newline.
export const tidy = md => md.split(/(^(?:```|~~~)[^\n]*\n[\s\S]*?^(?:```|~~~)[ \t]*$)/m)
  .map((part, i) => (i % 2 ? part : part.replace(/\n{3,}/g, '\n\n'))).join('').replace(/\s*$/, '\n')
