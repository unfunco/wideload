// Package wideload provides a slog.Handler that collects everything logged
// during a unit of work, such as an HTTP request, into one wide event.
//
// A wide event is a single structured record per unit of work. Fields are
// set by logging through the Handler or by calling Set. A later value for a
// key replaces an earlier one, groups are kept, and the event is emitted
// once, by Flush, at the highest level seen, with its duration and a summary
// of the log calls that fed it. A record whose context carries no buffer, or
// whose buffer has already been flushed, passes straight through to the
// wrapped handler.
package wideload

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options configures a Handler. A nil *Options is equivalent to the zero
// value.
type Options struct {
	// Level is the lowest level captured into a live buffer. The wrapped
	// handler's level does not apply until the event is emitted, so detail
	// below it is kept for the events that turn out to matter. A nil Level
	// captures every level.
	Level slog.Leveler

	// Message is the emitted record's message. It defaults to "Wide event".
	Message string

	// KeepMessages adds every captured message, prefixed by its level, to
	// the event as log.messages.
	KeepMessages bool

	// MaxFields caps the number of distinct fields captured from log records
	// into one event. Fields beyond it are dropped and counted in
	// log.dropped. Fields added with Set are not counted. Zero means no
	// limit.
	MaxFields int
}

const defaultMessage = "Wide event"

// Handler is a slog.Handler that captures records into the buffer carried by
// their context and emits them as one wide event on Flush.
type Handler struct {
	root   slog.Handler
	next   slog.Handler
	opts   Options
	bound  []field
	groups []string
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler returns a Handler that emits wide events to next.
func NewHandler(next slog.Handler, opts *Options) *Handler {
	h := &Handler{root: next, next: next}
	if opts != nil {
		h.opts = *opts
	}
	if h.opts.Message == "" {
		h.opts.Message = defaultMessage
	}
	return h
}

// Enabled reports whether a record at level would be captured or handled.
// While ctx carries a live buffer it applies Options.Level, so the wrapped
// handler's level cannot discard detail before the event is emitted.
// Otherwise it delegates to the wrapped handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	if buf, ok := fromContext(ctx); ok && buf.live() {
		return h.opts.Level == nil || level >= h.opts.Level.Level()
	}
	return h.next.Enabled(ctx, level)
}

// Handle captures the record into the buffer carried by ctx, or passes it to
// the wrapped handler when there is none or it has been flushed.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	buf, ok := fromContext(ctx)
	if !ok {
		return h.next.Handle(ctx, r)
	}
	buf.mu.Lock()
	if buf.flushed {
		buf.mu.Unlock()
		return h.next.Handle(ctx, r)
	}
	defer buf.mu.Unlock()
	for _, f := range h.bound {
		buf.set(f.path, f.attr, h.opts.MaxFields)
	}
	r.Attrs(func(a slog.Attr) bool {
		buf.set(h.groups, a, h.opts.MaxFields)
		return true
	})
	buf.record(r.Level, r.Message, h.opts.KeepMessages)
	return nil
}

// WithAttrs returns a Handler that adds attrs to every event its records are
// captured into, and to the wrapped handler for records that pass through.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	c := h.clone()
	c.next = h.next.WithAttrs(attrs)
	for _, a := range attrs {
		a.Value = a.Value.Resolve()
		c.bound = append(c.bound, field{path: h.groups, attr: a})
	}
	return c
}

// WithGroup returns a Handler that nests the attributes of the records it
// captures under name.
func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := h.clone()
	c.next = h.next.WithGroup(name)
	c.groups = join(h.groups, name)
	return c
}

func (h *Handler) clone() *Handler {
	c := *h
	c.bound = slices.Clone(h.bound)
	return &c
}

// Flush emits the buffer carried by ctx to the wrapped handler as one record
// at the highest level captured, and at least Info, so an event fed only by
// debug records still reaches an Info-level handler. It adds
// duration_ms, the time since NewContext, and a log group with count, the
// number of records captured, error_count, first_error when there was one,
// dropped when MaxFields was reached and messages when KeepMessages is set.
// Fields set explicitly under those keys are kept. The event is emitted only
// if the wrapped handler is enabled at its level. A second Flush of the same
// context does nothing, and records logged after it pass through. Flush
// returns the wrapped handler's error, or nil when ctx carries no buffer.
func (h *Handler) Flush(ctx context.Context) error {
	buf, ok := fromContext(ctx)
	if !ok {
		return nil
	}
	buf.mu.Lock()
	if buf.flushed {
		buf.mu.Unlock()
		return nil
	}
	buf.flushed = true
	level := max(buf.maxLevel, slog.LevelInfo)
	elapsed := time.Since(buf.start)
	ms := float64(elapsed) / float64(time.Millisecond)
	buf.set(nil, slog.Float64("duration_ms", ms), summary)
	buf.set(logGroup, slog.Int("count", buf.count), summary)
	buf.set(logGroup, slog.Int("error_count", buf.errors), summary)
	if buf.errors > 0 {
		buf.set(logGroup, slog.String("first_error", buf.firstError), summary)
	}
	if buf.dropped > 0 {
		buf.set(logGroup, slog.Int("dropped", buf.dropped), summary)
	}
	if len(buf.messages) > 0 {
		buf.set(logGroup, slog.Any("messages", buf.messages), summary)
	}
	attrs := nest(buf.fields)
	buf.mu.Unlock()

	if !h.root.Enabled(ctx, level) {
		return nil
	}
	r := slog.NewRecord(time.Now(), level, h.opts.Message, 0)
	r.AddAttrs(attrs...)
	return h.root.Handle(ctx, r)
}

// Middleware returns a handler that gives each request its own buffer, sets
// the request's method, path, pattern, status and response_bytes under http,
// raises the event to Error for a 5xx response or a panic, and flushes it
// when next returns. A panic is re-raised once the event is emitted.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := NewContext(r.Context())
		r = r.WithContext(ctx)
		Set(ctx, slog.Group("http",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		))
		rw := &responseWriter{ResponseWriter: w}
		defer func() {
			p := recover()
			status := rw.status
			if status == 0 && p == nil {
				status = http.StatusOK
			}
			attrs := make([]slog.Attr, 0, 3)
			if r.Pattern != "" {
				attrs = append(attrs, slog.String("pattern", r.Pattern))
			}
			if status != 0 {
				attrs = append(attrs, slog.Int("status", status))
			}
			attrs = append(attrs, slog.Int64("response_bytes", rw.bytes))
			Set(ctx, slog.Attr{Key: "http", Value: slog.GroupValue(attrs...)})
			if status >= http.StatusInternalServerError {
				SetLevel(ctx, slog.LevelError)
			}
			if p != nil {
				Set(ctx, slog.Any("panic", p))
				SetLevel(ctx, slog.LevelError)
			}
			_ = h.Flush(ctx)
			if p != nil {
				panic(p)
			}
		}()
		next.ServeHTTP(rw, r)
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status == 0 && code >= http.StatusOK {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

func (w *responseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// NewContext returns a context that carries a new, empty wide-event buffer.
// Its duration is measured from this call.
func NewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, &buffer{
		start: time.Now(),
		index: map[string]int{},
	})
}

// Set adds fields to the event carried by ctx without logging a message. A
// later value for a key replaces an earlier one and group attributes nest.
// It does nothing when ctx carries no buffer or the buffer has been flushed.
func Set(ctx context.Context, attrs ...slog.Attr) {
	buf, ok := fromContext(ctx)
	if !ok {
		return
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.flushed {
		return
	}
	for _, a := range attrs {
		buf.set(nil, a, 0)
	}
}

// SetLevel raises the level the event carried by ctx will be emitted at to
// level, if that is higher than the level already captured. It does nothing
// when ctx carries no buffer or the buffer has been flushed.
func SetLevel(ctx context.Context, level slog.Level) {
	buf, ok := fromContext(ctx)
	if !ok {
		return
	}
	buf.mu.Lock()
	defer buf.mu.Unlock()
	if buf.flushed {
		return
	}
	buf.raise(level)
}

type ctxKey struct{}

func fromContext(ctx context.Context) (*buffer, bool) {
	b, ok := ctx.Value(ctxKey{}).(*buffer)
	return b, ok
}

var logGroup = []string{"log"}

// summary marks a field Flush adds, which never replaces a value the caller
// set and is never dropped.
const summary = -1

type field struct {
	path []string
	attr slog.Attr
}

type buffer struct {
	mu         sync.Mutex
	start      time.Time
	fields     []field
	index      map[string]int
	dropped    int
	count      int
	errors     int
	firstError string
	maxLevel   slog.Level
	messages   []string
	flushed    bool
}

func (b *buffer) live() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.flushed
}

// set records a under path. A group attribute is flattened into its members
// under its key, as slog handlers do. limit caps the fields held, zero means
// no cap and summary marks a field Flush adds.
func (b *buffer) set(path []string, a slog.Attr, limit int) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			path = join(path, a.Key)
		}
		for _, ga := range a.Value.Group() {
			b.set(path, ga, limit)
		}
		return
	}
	id := identity(path, a.Key)
	if i, ok := b.index[id]; ok {
		if limit != summary {
			b.fields[i].attr = a
		}
		return
	}
	if limit > 0 && len(b.fields) >= limit {
		b.dropped++
		return
	}
	b.index[id] = len(b.fields)
	b.fields = append(b.fields, field{path: path, attr: a})
}

func (b *buffer) record(level slog.Level, msg string, keep bool) {
	b.count++
	if level >= slog.LevelError {
		b.errors++
		if b.errors == 1 {
			b.firstError = msg
		}
	}
	b.raise(level)
	if keep {
		b.messages = append(b.messages, level.String()+": "+msg)
	}
}

func (b *buffer) raise(level slog.Level) {
	b.maxLevel = max(b.maxLevel, level)
}

func join(path []string, name string) []string {
	p := make([]string, len(path)+1)
	copy(p, path)
	p[len(path)] = name
	return p
}

func identity(path []string, key string) string {
	var sb strings.Builder
	for _, p := range path {
		sb.WriteString(p)
		sb.WriteByte(0)
	}
	sb.WriteString(key)
	return sb.String()
}

// nest rebuilds group attributes from flattened fields, keeping the order in
// which each key and each group was first set.
func nest(fields []field) []slog.Attr {
	out := make([]slog.Attr, 0, len(fields))
	slot := map[string]int{}
	children := map[string][]field{}
	var order []string
	for _, f := range fields {
		if len(f.path) == 0 {
			out = append(out, f.attr)
			continue
		}
		g := f.path[0]
		if _, ok := slot[g]; !ok {
			slot[g] = len(out)
			out = append(out, slog.Attr{})
			order = append(order, g)
		}
		children[g] = append(children[g], field{path: f.path[1:], attr: f.attr})
	}
	for _, g := range order {
		out[slot[g]] = slog.Attr{
			Key:   g,
			Value: slog.GroupValue(nest(children[g])...),
		}
	}
	return out
}
