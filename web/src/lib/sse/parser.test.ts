import { describe, expect, it } from 'vitest';
import { SSEParser } from './parser';

describe('SSEParser', () => {
  it('parses events split across chunks', () => {
    const p = new SSEParser();
    expect(p.push('retry: 3000\n\nid: 4\nevent: lo')).toEqual([]);
    expect(p.push('g\ndata: {"seq":4}\n\n: ping\n\nevent: end\ndata: {}\n\n')).toEqual([
      { event: 'log', data: '{"seq":4}', id: '4' },
      { event: 'end', data: '{}', id: '4' },
    ]);
  });

  it('joins multi-line data, handles CRLF and defaults the event name', () => {
    const p = new SSEParser();
    expect(p.push('data: a\r\ndata: b\r\n\r\ndata:c\n\n')).toEqual([
      { event: 'message', data: 'a\nb' },
      { event: 'message', data: 'c' },
    ]);
  });

  it('ignores comments and blank events', () => {
    expect(new SSEParser().push(': ping\n\n\n\n')).toEqual([]);
  });
});
