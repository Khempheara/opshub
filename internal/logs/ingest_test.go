package logs

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLine(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	attrs := func(e entry) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal(e.attrs, &m))
		return m
	}

	e, rule := parseLine([]byte(`{"ts":"2026-09-26T11:59:00Z","level":"WARNING","msg":"slow query","ms":812,"attributes":{"db":"main","ms":1}}`), now)
	require.Empty(t, rule)
	assert.Equal(t, time.Date(2026, 9, 26, 11, 59, 0, 0, time.UTC), e.ts.UTC())
	assert.Equal(t, "warn", e.level)
	assert.Equal(t, "slow query", e.message)
	assert.Equal(t, map[string]any{"ms": 812.0, "db": "main"}, attrs(e), "top-level fields win over nested attributes")

	cases := []struct {
		line  string
		level string
		ts    time.Time
	}{
		{`{"message":"a","severity":"fatal"}`, "error", now},
		{`{"message":"a","level":"trace"}`, "debug", now},
		{`{"message":"a","level":"shouting"}`, "info", now},
		{`{"message":"a","level":3}`, "info", now},
		{`{"log":"a","time":1790423940}`, "info", time.Unix(1790423940, 0)},
		{`{"message":"a","timestamp":1790423940500}`, "info", time.Unix(1790423940, 500e6)},
		{`{"message":"a","ts":"2026-09-26T13:00:00Z"}`, "info", now}, // an hour ahead: server time
		{`{"message":"a","ts":"2026-09-26T12:04:00Z"}`, "info", now.Add(4 * time.Minute)},
	}
	for _, c := range cases {
		e, rule := parseLine([]byte(c.line), now)
		require.Empty(t, rule, c.line)
		assert.Equal(t, c.level, e.level, c.line)
		assert.True(t, c.ts.Equal(e.ts), "%s: %v", c.line, e.ts)
	}

	e, rule = parseLine([]byte(`{"message":{"nested":true}}`), now)
	require.Empty(t, rule)
	assert.JSONEq(t, `{"nested":true}`, e.message, "non-string messages keep their JSON")

	e, rule = parseLine([]byte(`{"message":"`+strings.Repeat("ក", 4000)+`","k":"a\u0000b"}`), now)
	require.Empty(t, rule)
	assert.LessOrEqual(t, len(e.message), MaxMessageBytes+len("…"))
	assert.True(t, strings.HasSuffix(e.message, "…"))
	assert.Equal(t, "ab", attrs(e)["k"], "NUL is removed")

	rejects := map[string]string{
		`not json`:                         "json_object",
		`["a"]`:                            "json_object",
		`null`:                             "json_object",
		`{"level":"info"}`:                 "message_required",
		`{"message":"   "}`:                "message_required",
		`{"message":"a","ts":"yesterday"}`: "ts_format",
		`{"message":"a","ts":-5}`:          "ts_format",
		`{"message":"a","ts":"2026-09-01T00:00:00Z"}`:                       "too_old",
		`{"message":"a","big":"` + strings.Repeat("x", MaxAttrBytes) + `"}`: "attributes_max",
	}
	for line, want := range rejects {
		_, rule := parseLine([]byte(line), now)
		assert.Equal(t, want, rule, line)
	}
	many := map[string]int{"message": 1}
	for i := range MaxAttributeKeys + 1 {
		many["k"+strings.Repeat("x", i)] = i
	}
	raw, _ := json.Marshal(many)
	_, rule = parseLine(raw, now)
	assert.Equal(t, "attributes_max", rule)
}

func TestClip(t *testing.T) {
	assert.Equal(t, "abc", clip("abc", 3))
	assert.Equal(t, "ab…", clip("abc", 2))
	assert.Equal(t, "…", clip("ក", 2), "never splits a rune")
}
