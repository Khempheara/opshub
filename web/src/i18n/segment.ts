/**
 * Khmer is written without spaces between words, so browsers can only wrap it where
 * their line-breaker knows the word boundaries. For narrow containers (table cells,
 * badges, toasts) we insert ZERO WIDTH SPACE (U+200B) at dictionary word boundaries
 * found by Intl.Segmenter, giving every engine a legal break opportunity.
 *
 * Only boundaries between two Khmer segments are touched; Latin text, code and
 * resource names are returned unchanged.
 */
const ZWSP = '​';
const KHMER = /[ក-៿᧠-᧿]/;

let segmenter: Intl.Segmenter | null | undefined;
function getSegmenter(): Intl.Segmenter | null {
  if (segmenter === undefined) {
    segmenter = typeof Intl.Segmenter === 'function' ? new Intl.Segmenter('km', { granularity: 'word' }) : null;
  }
  return segmenter;
}

export function insertWordBreaks(text: string): string {
  if (!KHMER.test(text)) return text;
  const seg = getSegmenter();
  if (!seg) return text;

  let out = '';
  let prevKhmer = false;
  for (const { segment } of seg.segment(text)) {
    const isKhmer = KHMER.test(segment);
    if (prevKhmer && isKhmer && !out.endsWith(ZWSP)) out += ZWSP;
    out += segment;
    prevKhmer = isKhmer;
  }
  return out;
}
