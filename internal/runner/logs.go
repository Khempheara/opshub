package runner

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Log shipping: output is batched about once a second or every 64 KiB, cut at line ends
// where possible so masking sees whole lines.
const (
	logFlushInterval = time.Second
	logChunkBytes    = 64 << 10
	logMaxPending    = 4 << 20 // beyond this, output is dropped until the server catches up
)

// LogSender sends one chunk; it's Client.AppendLog bound to a job.
type LogSender func(ctx context.Context, seq int, content string) error

// LogShipper is an io.Writer that ships job output to OpsHub.
type LogShipper struct {
	send    LogSender
	masks   []string
	onGone  func() // the server says the job isn't running any more
	mu      sync.Mutex
	buf     bytes.Buffer
	dropped bool
	seq     int
	kick    chan struct{}
	done    chan struct{}
	stopped chan struct{}
}

// NewLogShipper starts shipping; call Close to flush the rest.
func NewLogShipper(send LogSender, masks []string, onGone func()) *LogShipper {
	m := slices.Clone(masks)
	slices.SortFunc(m, func(a, b string) int { return len(b) - len(a) })
	l := &LogShipper{send: send, masks: m, onGone: onGone, kick: make(chan struct{}, 1), done: make(chan struct{}), stopped: make(chan struct{})}
	go l.loop()
	return l
}

// Write buffers output. It never fails, so a slow server can't stall the job.
func (l *LogShipper) Write(p []byte) (int, error) {
	l.mu.Lock()
	if l.buf.Len()+len(p) > logMaxPending {
		if !l.dropped {
			l.buf.WriteString("\n[opshub-runner: output dropped while the server was catching up]\n")
			l.dropped = true
		}
	} else {
		l.dropped = false
		l.buf.Write(bytes.ReplaceAll(p, []byte{0}, nil)) // NUL can't be stored as text
	}
	full := l.buf.Len() >= logChunkBytes
	l.mu.Unlock()
	if full {
		select {
		case l.kick <- struct{}{}:
		default:
		}
	}
	return len(p), nil
}

// Printf-style helper for runner messages (bold, so they stand out from step output).
func (l *LogShipper) Notice(msg string) {
	_, _ = l.Write([]byte("\x1b[1m" + msg + "\x1b[0m\n"))
}

func (l *LogShipper) loop() {
	defer close(l.stopped)
	t := time.NewTicker(logFlushInterval)
	defer t.Stop()
	for {
		select {
		case <-l.done:
			for l.flush(true) {
			}
			return
		case <-t.C:
			for l.flush(false) {
			}
		case <-l.kick:
			for l.flush(false) {
			}
		}
	}
}

// flush sends one chunk and reports whether more is ready.
func (l *LogShipper) flush(final bool) bool {
	l.mu.Lock()
	b := l.buf.Bytes()
	n := len(b)
	if n > logChunkBytes {
		n = logChunkBytes
	}
	if !final || n < len(b) {
		// Prefer to end at a newline; otherwise at a UTF-8 boundary.
		if i := bytes.LastIndexByte(b[:n], '\n'); i >= 0 {
			n = i + 1
		} else if n < logChunkBytes && !final {
			n = 0 // a partial line: wait for more (unless the buffer is full)
		} else {
			for n > 0 && n < len(b) && !utf8.RuneStart(b[n]) {
				n--
			}
		}
	}
	if n == 0 {
		l.mu.Unlock()
		return false
	}
	chunk := string(b[:n])
	l.buf.Next(n)
	more := l.buf.Len() >= logChunkBytes || (final && l.buf.Len() > 0)
	l.seq++
	seq := l.seq
	l.mu.Unlock()

	for _, m := range l.masks {
		if len(m) >= 4 {
			chunk = strings.ReplaceAll(chunk, m, "••••••")
		}
	}
	// A few retries; the log is best effort, the job's result is what matters.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for attempt := range 4 {
		err := l.send(ctx, seq, chunk)
		if err == nil || ctx.Err() != nil {
			break
		}
		if IsCode(err, "JOB_NOT_RUNNING") || IsCode(err, "JOB_TOKEN_INVALID") {
			if l.onGone != nil {
				l.onGone()
			}
			break
		}
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	return more
}

// Close flushes what's buffered and stops shipping.
func (l *LogShipper) Close() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
	<-l.stopped
}
