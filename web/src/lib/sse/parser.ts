/** One Server-Sent Event. */
export interface SSEMessage {
  event: string;
  data: string;
  id?: string;
}

/**
 * Incremental text/event-stream parser (WHATWG "event stream interpretation"): feed it
 * decoded text as it arrives; it returns the events completed so far. Comments (": ping")
 * and unknown fields are ignored.
 */
export class SSEParser {
  private buffer = '';
  private event = '';
  private data: string[] = [];
  private id: string | undefined;

  push(chunk: string): SSEMessage[] {
    this.buffer += chunk;
    const out: SSEMessage[] = [];
    let nl: number;
    while ((nl = this.buffer.search(/\r\n|\r|\n/)) >= 0) {
      const line = this.buffer.slice(0, nl);
      const sep = this.buffer.startsWith('\r\n', nl) ? 2 : 1;
      this.buffer = this.buffer.slice(nl + sep);
      if (line === '') {
        if (this.data.length > 0 || this.event) {
          out.push({ event: this.event || 'message', data: this.data.join('\n'), ...(this.id !== undefined ? { id: this.id } : {}) });
        }
        this.event = '';
        this.data = [];
        continue;
      }
      if (line.startsWith(':')) continue;
      const colon = line.indexOf(':');
      const field = colon < 0 ? line : line.slice(0, colon);
      let value = colon < 0 ? '' : line.slice(colon + 1);
      if (value.startsWith(' ')) value = value.slice(1);
      if (field === 'event') this.event = value;
      else if (field === 'data') this.data.push(value);
      else if (field === 'id' && !value.includes('\0')) this.id = value;
    }
    return out;
  }
}
