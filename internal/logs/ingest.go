package logs

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/opshub/opshub/internal/store"
)

// Ingest limits.
const (
	MaxBodyBytes     = 1 << 20 // per request
	MaxLines         = 5000
	MaxMessageBytes  = 8 << 10 // longer messages are clipped
	MaxAttributeKeys = 50
	MaxAttrBytes     = 8 << 10
	// MaxAge is how far back a line's timestamp may be (partitions exist for this window).
	MaxAge = 7 * 24 * time.Hour
	// MaxSkew: timestamps further in the future are replaced by the server's time.
	MaxSkew     = 5 * time.Minute
	maxProblems = 20
)

// LineError is one rejected line (1-based).
type LineError struct {
	Line int    `json:"line"`
	Rule string `json:"rule"`
}

// IngestResult is the answer to POST /ingest/logs.
type IngestResult struct {
	Accepted int         `json:"accepted"`
	Rejected int         `json:"rejected"`
	Errors   []LineError `json:"errors"` // the first 20
}

// ErrTooLarge means the body is over MaxBodyBytes or MaxLines.
var ErrTooLarge = errors.New("log batch too large")

// Line fields: the first of each group that is present is used; every other field becomes an
// attribute, so the output of common shippers works unchanged.
var (
	tsKeys      = []string{"ts", "timestamp", "time", "@timestamp"}
	levelKeys   = []string{"level", "severity", "lvl"}
	messageKeys = []string{"message", "msg", "log"}
)

var levels = map[string]string{
	"trace": "debug", "debug": "debug",
	"info": "info", "information": "info", "notice": "info",
	"warn": "warn", "warning": "warn",
	"error": "error", "err": "error", "fatal": "error", "critical": "error", "crit": "error", "panic": "error", "alert": "error", "emerg": "error",
}

// entry is one parsed line.
type entry struct {
	ts      time.Time
	level   string
	message string
	attrs   []byte
}

func pick(m map[string]json.RawMessage, keys []string) (json.RawMessage, bool) {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			for _, kk := range keys {
				delete(m, kk)
			}
			return v, true
		}
	}
	return nil, false
}

// parseTime accepts RFC 3339 strings and Unix times in seconds or milliseconds.
func parseTime(raw json.RawMessage) (time.Time, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		t, err := time.Parse(time.RFC3339Nano, s)
		return t, err == nil
	}
	var f float64
	if json.Unmarshal(raw, &f) != nil || math.IsNaN(f) || f <= 0 {
		return time.Time{}, false
	}
	if f > 1e12 { // milliseconds
		f /= 1000
	}
	sec, frac := math.Modf(f)
	return time.Unix(int64(sec), int64(frac*1e9)), true
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}

// parseLine turns one NDJSON line into an entry, or a rule explaining why it was rejected.
func parseLine(line []byte, now time.Time) (entry, string) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(line, &m); err != nil || m == nil {
		return entry{}, "json_object"
	}
	e := entry{ts: now, level: "info"}
	raw, ok := pick(m, messageKeys)
	if !ok {
		return entry{}, "message_required"
	}
	if err := json.Unmarshal(raw, &e.message); err != nil {
		e.message = string(raw) // a number or object: keep its JSON
	}
	e.message = clip(strings.ToValidUTF8(strings.ReplaceAll(e.message, "\x00", ""), "�"), MaxMessageBytes)
	if strings.TrimSpace(e.message) == "" {
		return entry{}, "message_required"
	}
	if raw, ok := pick(m, levelKeys); ok {
		var l string
		_ = json.Unmarshal(raw, &l)
		if v, known := levels[strings.ToLower(strings.TrimSpace(l))]; known {
			e.level = v
		}
	}
	if raw, ok := pick(m, tsKeys); ok {
		t, valid := parseTime(raw)
		switch {
		case !valid:
			return entry{}, "ts_format"
		case t.Before(now.Add(-MaxAge)):
			return entry{}, "too_old"
		case t.After(now.Add(MaxSkew)):
			// A clock far ahead: keep the line at the server's time.
		default:
			e.ts = t
		}
	}
	// Nested "attributes" merge into the rest of the fields.
	if raw, ok := m["attributes"]; ok {
		delete(m, "attributes")
		var nested map[string]json.RawMessage
		if json.Unmarshal(raw, &nested) == nil {
			for k, v := range nested {
				if _, taken := m[k]; !taken {
					m[k] = v
				}
			}
		}
	}
	if len(m) > MaxAttributeKeys {
		return entry{}, "attributes_max"
	}
	attrs, err := json.Marshal(m)
	if err != nil || len(attrs) > MaxAttrBytes {
		return entry{}, "attributes_max"
	}
	e.attrs = bytes.ReplaceAll(attrs, []byte(`\u0000`), nil) // jsonb can't hold NUL
	return e, ""
}

// Ingest stores a batch of NDJSON lines for a token's service. Bad lines are reported and
// skipped; the rest are stored in one statement.
func (s *Service) Ingest(ctx context.Context, t store.LogIngestToken, body io.Reader) (IngestResult, error) {
	res := IngestResult{Errors: []LineError{}}
	now := s.now()
	body = &countingReader{r: io.LimitReader(body, MaxBodyBytes+1)}
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 64<<10), MaxBodyBytes+1)
	var (
		ts       []time.Time
		lvls     []string
		messages []string
		attrs    [][]byte
		n        int
	)
	for sc.Scan() {
		n++
		if n > MaxLines {
			return IngestResult{}, ErrTooLarge
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		e, rule := parseLine(line, now)
		if rule != "" {
			res.Rejected++
			if len(res.Errors) < maxProblems {
				res.Errors = append(res.Errors, LineError{Line: n, Rule: rule})
			}
			continue
		}
		ts, lvls, messages, attrs = append(ts, e.ts), append(lvls, e.level), append(messages, e.message), append(attrs, e.attrs)
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return IngestResult{}, ErrTooLarge
		}
		return IngestResult{}, fmt.Errorf("read logs: %w", err)
	}
	if body.(*countingReader).n > MaxBodyBytes {
		return IngestResult{}, ErrTooLarge
	}
	if len(ts) > 0 {
		q := store.New(s.pool)
		stored, err := q.InsertServiceLogs(ctx, store.InsertServiceLogsParams{
			OrganizationID: t.OrganizationID, Ts: ts, SourceID: t.ID, Service: t.Service, Levels: lvls, Messages: messages, Attributes: attrs,
		})
		if err != nil {
			return IngestResult{}, err
		}
		res.Accepted = int(stored)
		if err := q.TouchIngestToken(ctx, t.ID); err != nil {
			return IngestResult{}, err
		}
	}
	return res, nil
}

type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
