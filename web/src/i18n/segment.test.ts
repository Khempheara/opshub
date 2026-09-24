import { describe, expect, it } from 'vitest';
import { insertWordBreaks } from './segment';

const ZWSP = '​';

describe('insertWordBreaks', () => {
  it('inserts break opportunities between Khmer words', () => {
    const text = 'ការដាក់ឱ្យដំណើរការបានបរាជ័យ'; // "the deployment failed", no spaces
    const out = insertWordBreaks(text);
    expect(out).toContain(ZWSP);
    expect(out.replaceAll(ZWSP, '')).toBe(text); // content is unchanged
  });

  it('never splits a Khmer cluster (subscript consonants stay attached)', () => {
    const out = insertWordBreaks('ស្ថានភាពប្រព័ន្ធ');
    for (const part of out.split(ZWSP)) {
      // A part must not start with a COENG (U+17D2) or a dependent vowel/sign.
      expect(part).not.toMatch(/^[ា-៓]/u);
    }
  });

  it('leaves Latin text, code and mixed boundaries alone', () => {
    expect(insertWordBreaks('Pipeline #42 failed')).toBe('Pipeline #42 failed');
    expect(insertWordBreaks('docker build -t app:${SHA} .')).toBe('docker build -t app:${SHA} .');
    const mixed = insertWordBreaks('Deploy ទៅ production');
    expect(mixed).toBe('Deploy ទៅ production');
  });
});
