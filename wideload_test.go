package wideload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"

	"github.com/unfunco/wideload"
)

func newLogger(
	out *bytes.Buffer, level slog.Level, opts *wideload.Options,
) (*slog.Logger, *wideload.Handler) {
	sink := slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level})
	h := wideload.NewHandler(sink, opts)
	return slog.New(h), h
}

func decode(t *testing.T, out *bytes.Buffer) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || lines[0] == "" {
		t.Fatalf("expected one line of output, got %q", out.String())
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &m); err != nil {
		t.Fatalf("decode %q: %v", lines[0], err)
	}
	return m
}

func group(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	g, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("expected %q to be a group, got %#v", key, m[key])
	}
	return g
}

func TestEmitsOneEventAtTheHighestLevel(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	logger.InfoContext(ctx, "step 1", "key1", "value1")
	logger.WarnContext(ctx, "step 2", "count", 42)
	if out.Len() > 0 {
		t.Fatalf("expected no output before Flush, got %q", out.String())
	}

	if err := h.Flush(ctx); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	m := decode(t, &out)
	if m["level"] != "WARN" || m["msg"] != "Wide event" {
		t.Errorf("level=%v msg=%v", m["level"], m["msg"])
	}
	if m["key1"] != "value1" || m["count"] != 42.0 {
		t.Errorf("fields: %v", m)
	}
	if _, ok := m["duration_ms"].(float64); !ok {
		t.Errorf("duration_ms missing: %v", m)
	}
	l := group(t, m, "log")
	if l["count"] != 2.0 || l["error_count"] != 0.0 {
		t.Errorf("log summary: %v", l)
	}
	if _, ok := l["first_error"]; ok {
		t.Errorf("first_error set with no errors: %v", l)
	}
}

func TestLastWriteWins(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	logger.InfoContext(ctx, "a", "user_id", 1, "first", true)
	logger.InfoContext(ctx, "b", "user_id", 2)
	wideload.Set(ctx, slog.Int("user_id", 3))
	_ = h.Flush(ctx)

	line := out.String()
	if strings.Count(line, `"user_id"`) != 1 {
		t.Errorf("duplicate key in %s", line)
	}
	m := decode(t, &out)
	if m["user_id"] != 3.0 {
		t.Errorf("user_id=%v, want 3", m["user_id"])
	}
	if !strings.HasPrefix(line, `{"time":`) ||
		strings.Index(line, `"user_id"`) > strings.Index(line, `"first"`) {
		t.Errorf("first-set order lost in %s", line)
	}
}

func TestBoundAttrsAndGroups(t *testing.T) {
	var out bytes.Buffer
	root, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	req := root.With("request_id", "abc")
	req.InfoContext(ctx, "start", "route", "/x")
	req.WithGroup("http").InfoContext(ctx, "resp", "status", 500,
		slog.Group("client", "ip", "10.0.0.1"))
	req.WithGroup("http").WithGroup("client").InfoContext(ctx, "again",
		"ip", "10.0.0.2", "ua", "curl")
	root.InfoContext(ctx, "flat", slog.Group("", "inlined", true))
	_ = h.Flush(ctx)

	m := decode(t, &out)
	if m["request_id"] != "abc" || m["route"] != "/x" || m["inlined"] != true {
		t.Errorf("top level: %v", m)
	}
	httpG := group(t, m, "http")
	if httpG["status"] != 500.0 {
		t.Errorf("http.status=%v", httpG["status"])
	}
	client := group(t, httpG, "client")
	if client["ip"] != "10.0.0.2" || client["ua"] != "curl" {
		t.Errorf("http.client=%v", client)
	}
	if _, ok := httpG["request_id"]; ok {
		t.Errorf("bound attr nested under a later group: %v", httpG)
	}
}

func TestCapturesDetailBelowTheSinkLevel(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	logger.DebugContext(ctx, "sql", "query", "SELECT 1")
	logger.ErrorContext(ctx, "failed", "err", "boom")
	_ = h.Flush(ctx)

	m := decode(t, &out)
	if m["query"] != "SELECT 1" || m["level"] != "ERROR" {
		t.Errorf("debug detail lost: %v", m)
	}
	l := group(t, m, "log")
	if l["error_count"] != 1.0 || l["first_error"] != "failed" {
		t.Errorf("log summary: %v", l)
	}
}

func TestLevelOptionLimitsCapture(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelDebug,
		&wideload.Options{Level: slog.LevelInfo})
	ctx := wideload.NewContext(context.Background())

	logger.DebugContext(ctx, "noise", "skip", true)
	logger.InfoContext(ctx, "signal", "keep", true)
	_ = h.Flush(ctx)

	m := decode(t, &out)
	if _, ok := m["skip"]; ok || m["keep"] != true {
		t.Errorf("Level option ignored: %v", m)
	}
}

func TestEventBelowTheSinkLevelIsNotEmitted(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelWarn, nil)
	ctx := wideload.NewContext(context.Background())

	logger.InfoContext(ctx, "quiet")
	if err := h.Flush(ctx); err != nil || out.Len() != 0 {
		t.Errorf("err=%v out=%q", err, out.String())
	}
}

func TestFlushOnceThenPassThrough(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	logger.InfoContext(ctx, "one")
	_ = h.Flush(ctx)
	_ = h.Flush(ctx)
	logger.InfoContext(ctx, "after", "late", true)
	wideload.Set(ctx, slog.Bool("ignored", true))

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected the event and one pass-through line, got %q",
			out.String())
	}
	if !strings.Contains(lines[0], `"msg":"Wide event"`) ||
		!strings.Contains(lines[1], `"msg":"after"`) ||
		strings.Contains(lines[0], "ignored") {
		t.Errorf("lines: %q", lines)
	}
	if !h.Enabled(ctx, slog.LevelInfo) || h.Enabled(ctx, slog.LevelDebug) {
		t.Errorf("Enabled should delegate to the sink after Flush")
	}
}

func TestSetAndSetLevelWithoutLogging(t *testing.T) {
	var out bytes.Buffer
	_, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())

	wideload.Set(ctx, slog.String("tenant", "t1"),
		slog.Group("db", "rows", 7))
	wideload.SetLevel(ctx, slog.LevelError)
	wideload.SetLevel(ctx, slog.LevelDebug)
	_ = h.Flush(ctx)

	m := decode(t, &out)
	if m["tenant"] != "t1" || group(t, m, "db")["rows"] != 7.0 {
		t.Errorf("fields: %v", m)
	}
	if m["level"] != "ERROR" {
		t.Errorf("level=%v, want ERROR", m["level"])
	}
	if group(t, m, "log")["count"] != 0.0 {
		t.Errorf("Set counted as a record: %v", m)
	}
}

func TestEventIsAtLeastInfo(t *testing.T) {
	for name, log := range map[string]func(context.Context, *slog.Logger){
		"empty": func(context.Context, *slog.Logger) {},
		"debug only": func(ctx context.Context, l *slog.Logger) {
			l.DebugContext(ctx, "detail", "k", "v")
		},
	} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			logger, h := newLogger(&out, slog.LevelInfo, nil)
			ctx := wideload.NewContext(context.Background())
			log(ctx, logger)
			_ = h.Flush(ctx)
			if m := decode(t, &out); m["level"] != "INFO" {
				t.Errorf("level=%v, want INFO", m["level"])
			}
		})
	}
}

func TestCallerFieldsBeatSummaryFields(t *testing.T) {
	var out bytes.Buffer
	_, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())
	wideload.Set(ctx, slog.Float64("duration_ms", 1.5),
		slog.Group("log", "count", 99))
	_ = h.Flush(ctx)
	m := decode(t, &out)
	if m["duration_ms"] != 1.5 || group(t, m, "log")["count"] != 99.0 {
		t.Errorf("summary overwrote the caller's fields: %v", m)
	}
}

type counter struct{ n *int }

func (c counter) LogValue() slog.Value { return slog.IntValue(*c.n) }

func TestValuesResolveOnCapture(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())
	n := 1
	logger.InfoContext(ctx, "x", "n", counter{&n})
	bound := logger.With("m", counter{&n})
	n = 2
	bound.InfoContext(ctx, "y")
	n = 99
	_ = h.Flush(ctx)
	m := decode(t, &out)
	if m["n"] != 1.0 || m["m"] != 1.0 {
		t.Errorf("values resolved late: n=%v m=%v", m["n"], m["m"])
	}
}

func TestMaxFieldsDropsAndCounts(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo,
		&wideload.Options{MaxFields: 2})
	ctx := wideload.NewContext(context.Background())
	logger.InfoContext(ctx, "x", "a", 1, "b", 2, "c", 3, "a", 4)
	wideload.Set(ctx, slog.Int("d", 5))
	_ = h.Flush(ctx)
	m := decode(t, &out)
	if m["a"] != 4.0 || m["b"] != 2.0 || m["d"] != 5.0 {
		t.Errorf("kept fields: %v", m)
	}
	if _, ok := m["c"]; ok {
		t.Errorf("c should have been dropped: %v", m)
	}
	if group(t, m, "log")["dropped"] != 1.0 {
		t.Errorf("dropped count: %v", m["log"])
	}
}

func TestKeepMessagesAndCustomMessage(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo,
		&wideload.Options{KeepMessages: true, Message: "request"})
	ctx := wideload.NewContext(context.Background())
	logger.InfoContext(ctx, "one")
	logger.ErrorContext(ctx, "two")
	_ = h.Flush(ctx)
	m := decode(t, &out)
	if m["msg"] != "request" {
		t.Errorf("msg=%v", m["msg"])
	}
	msgs, _ := group(t, m, "log")["messages"].([]any)
	if len(msgs) != 2 || msgs[0] != "INFO: one" || msgs[1] != "ERROR: two" {
		t.Errorf("messages: %v", msgs)
	}
}

func TestNoBufferPassesThrough(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	logger.Info("direct", "foo", "bar")
	m := decode(t, &out)
	if m["msg"] != "direct" || m["foo"] != "bar" {
		t.Errorf("pass-through: %v", m)
	}
	if err := h.Flush(context.Background()); err != nil {
		t.Errorf("Flush without a buffer: %v", err)
	}
}

func TestPassThroughConformsToSlogtest(t *testing.T) {
	var out bytes.Buffer
	h := wideload.NewHandler(slog.NewJSONHandler(&out, nil), nil)
	results := func() []map[string]any {
		var ms []map[string]any
		for _, line := range bytes.Split(out.Bytes(), []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal(line, &m); err != nil {
				t.Fatal(err)
			}
			ms = append(ms, m)
		}
		return ms
	}
	if err := slogtest.TestHandler(h, results); err != nil {
		t.Error(err)
	}
}

func TestConcurrentCapture(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	ctx := wideload.NewContext(context.Background())
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logger.InfoContext(ctx, "work", "worker", i, "shared", i)
			wideload.Set(ctx, slog.Int("set", i))
			logger.WithGroup("g").InfoContext(ctx, "grouped", "n", i)
		}()
	}
	wg.Wait()
	_ = h.Flush(ctx)
	m := decode(t, &out)
	if group(t, m, "log")["count"] != 100.0 {
		t.Errorf("count=%v, want 100", group(t, m, "log")["count"])
	}
}

func TestMiddleware(t *testing.T) {
	var out bytes.Buffer
	logger, h := newLogger(&out, slog.LevelInfo, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		logger.DebugContext(r.Context(), "lookup", "user_id", r.PathValue("id"))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("hello"))
	})
	mux.HandleFunc("GET /boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	mux.HandleFunc("GET /panic", func(http.ResponseWriter, *http.Request) {
		panic(errors.New("kaboom"))
	})
	srv := h.Middleware(mux)

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/users/7", nil))
	m := decode(t, &out)
	hg := group(t, m, "http")
	if m["level"] != "INFO" || m["user_id"] != "7" ||
		hg["method"] != "GET" || hg["path"] != "/users/7" ||
		hg["pattern"] != "GET /users/{id}" || hg["status"] != 201.0 ||
		hg["response_bytes"] != 5.0 {
		t.Errorf("ok request: %v", m)
	}

	out.Reset()
	srv.ServeHTTP(httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/boom", nil))
	m = decode(t, &out)
	if m["level"] != "ERROR" || group(t, m, "http")["status"] != 502.0 {
		t.Errorf("5xx request: %v", m)
	}

	out.Reset()
	func() {
		defer func() {
			if p := recover(); p == nil {
				t.Error("panic was swallowed")
			}
		}()
		srv.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/panic", nil))
	}()
	m = decode(t, &out)
	if m["level"] != "ERROR" || m["panic"] != "kaboom" {
		t.Errorf("panicking request: %v", m)
	}
	if _, ok := group(t, m, "http")["status"]; ok {
		t.Errorf("status reported for a response never written: %v", m)
	}
}
