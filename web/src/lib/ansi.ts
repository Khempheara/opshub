/** A run of log text with the same style. */
export interface AnsiSegment {
  text: string;
  fg?: number; // 0–15 (8–15 = bright)
  bg?: number;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
}

interface Style {
  fg?: number;
  bg?: number;
  bold?: boolean;
  dim?: boolean;
  italic?: boolean;
  underline?: boolean;
}

// eslint-disable-next-line no-control-regex -- ANSI escape sequences are control characters
const ESCAPE = /\u001b\[([0-9;]*)([A-Za-z])/g;

/**
 * Splits terminal output into styled segments. Supports SGR (colors 30–37/90–97, 40–47/
 * 100–107, bold, dim, italic, underline, resets); other escape sequences (cursor moves,
 * erase) are dropped. 256-color and truecolor codes are skipped without breaking parsing.
 */
export function parseAnsi(input: string): AnsiSegment[] {
  const out: AnsiSegment[] = [];
  let style: Style = {};
  let last = 0;
  const push = (text: string) => {
    if (!text) return;
    const prev = out.at(-1);
    if (prev && sameStyle(prev, style)) prev.text += text;
    else out.push({ text, ...style });
  };
  for (const m of input.matchAll(ESCAPE)) {
    push(input.slice(last, m.index));
    last = m.index + m[0].length;
    if (m[2] !== 'm') continue;
    const codes = (m[1] ?? '').split(';').map((c) => (c === '' ? 0 : Number(c)));
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i] ?? 0;
      if (c === 0) style = {};
      else if (c === 1) style = { ...style, bold: true };
      else if (c === 2) style = { ...style, dim: true };
      else if (c === 3) style = { ...style, italic: true };
      else if (c === 4) style = { ...style, underline: true };
      else if (c === 22) style = { ...style, bold: false, dim: false };
      else if (c === 23) style = { ...style, italic: false };
      else if (c === 24) style = { ...style, underline: false };
      else if (c >= 30 && c <= 37) style = { ...style, fg: c - 30 };
      else if (c >= 90 && c <= 97) style = { ...style, fg: c - 90 + 8 };
      else if (c === 39) style = { ...style, fg: undefined };
      else if (c >= 40 && c <= 47) style = { ...style, bg: c - 40 };
      else if (c >= 100 && c <= 107) style = { ...style, bg: c - 100 + 8 };
      else if (c === 49) style = { ...style, bg: undefined };
      else if (c === 38 || c === 48) i += codes[i + 1] === 5 ? 2 : 4; // 256-color / truecolor: skip
    }
  }
  push(input.slice(last));
  return out;
}

function sameStyle(a: AnsiSegment, b: Style): boolean {
  return a.fg === b.fg && a.bg === b.bg && !!a.bold === !!b.bold && !!a.dim === !!b.dim && !!a.italic === !!b.italic && !!a.underline === !!b.underline;
}
