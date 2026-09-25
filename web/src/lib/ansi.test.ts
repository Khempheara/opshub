import { describe, expect, it } from 'vitest';
import { parseAnsi } from './ansi';

const ESC = '\u001b';

describe('parseAnsi', () => {
  it('returns plain text unchanged', () => {
    expect(parseAnsi('hello\n')).toEqual([{ text: 'hello\n' }]);
  });

  it('applies and resets colors and bold', () => {
    expect(parseAnsi(`${ESC}[1;31mFAIL${ESC}[0m ok ${ESC}[92mPASS${ESC}[39m`)).toEqual([
      { text: 'FAIL', bold: true, fg: 1 },
      { text: ' ok ' },
      { text: 'PASS', fg: 10 },
    ]);
  });

  it('drops non-color sequences and skips 256/truecolor codes', () => {
    expect(parseAnsi(`${ESC}[2K${ESC}[1Gdone`)).toEqual([{ text: 'done' }]);
    expect(parseAnsi(`${ESC}[38;5;208morange${ESC}[38;2;1;2;3m rgb${ESC}[0m`)).toEqual([{ text: 'orange rgb' }]);
    expect(parseAnsi(`${ESC}[44;97mbadge`)).toEqual([{ text: 'badge', bg: 4, fg: 15 }]);
  });

  it('merges adjacent segments with the same style', () => {
    expect(parseAnsi(`${ESC}[33ma${ESC}[33mb`)).toEqual([{ text: 'ab', fg: 3 }]);
  });
});
