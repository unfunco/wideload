// Package wideload provides a slog.Handler that accumulates log records into
// a buffer stored in the context, allowing them to be emitted as a single
// wide log event later.
package wideload

import (
	"context"
	"log/slog"
	"sync"
)

// Handler implements the slog.Handler interface. It accumulates log records and
// attributes into a buffer stored in the context, instead of writing them
// immediately, they can be emitted later as a single wide log event.
type Handler struct {
	next slog.Handler
}

// NewHandler creates a new Handler wrapping the provided destination handler.
func NewHandler(next slog.Handler) *Handler {
	return &Handler{next: next}
}

// Enabled reports whether the handler handles records at the given level.
// It delegates to the underlying handler.
func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

// Handle handles the record. If a wide log buffer is found in the context,
// the record's attributes and message are added to it. If no buffer is found,
// it passes the record directly to the underlying handler.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	buf, ok := getBuffer(ctx)
	if !ok {
		return h.next.Handle(ctx, r)
	}

	buf.mu.Lock()
	defer buf.mu.Unlock()

	buf.messages = append(buf.messages, r.Message)

	r.Attrs(func(a slog.Attr) bool {
		buf.attrs = append(buf.attrs, a)
		return true
	})

	if r.Level > buf.maxLevel {
		buf.maxLevel = r.Level
	}

	return nil
}

// WithAttrs returns a new Handler with the given attributes added.
func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{next: h.next.WithAttrs(attrs)}
}

// WithGroup returns a new Handler with the given group.
func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{next: h.next.WithGroup(name)}
}

type buffer struct {
	mu       sync.Mutex
	messages []string
	attrs    []slog.Attr
	maxLevel slog.Level
}

type ctxKey struct{}

// NewContext returns a new context that carries a wide log buffer.
func NewContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, ctxKey{}, &buffer{
		// Start with the lowest level to capture everything.
		maxLevel: slog.LevelDebug,
	})
}

func getBuffer(ctx context.Context) (*buffer, bool) {
	b, ok := ctx.Value(ctxKey{}).(*buffer)
	return b, ok
}

// Flush emits the accumulated log data as a single record to the underlying
// handler at the highest log level recorded. If no buffer is found in the
// context, Flush does nothing.
func Flush(ctx context.Context, logger *slog.Logger) {
	buf, ok := getBuffer(ctx)
	if !ok {
		return
	}

	buf.mu.Lock()
	messages := make([]string, len(buf.messages))
	copy(messages, buf.messages)
	attrs := make([]slog.Attr, len(buf.attrs))
	copy(attrs, buf.attrs)
	maxLevel := buf.maxLevel
	buf.mu.Unlock()

	if len(messages) == 0 && len(attrs) == 0 {
		return
	}

	var msg string
	if len(messages) > 0 {
		msg = messages[len(messages)-1]
	} else {
		msg = "Wide Event"
	}

	allMessages := slog.Any("logs", messages)
	finalAttrs := make([]slog.Attr, 0, len(attrs)+1)
	finalAttrs = append(finalAttrs, allMessages)
	finalAttrs = append(finalAttrs, attrs...)

	// Create a context that hides the buffer to avoid infinite recursion
	// and ensure this log goes straight to the underlying handler.
	flushCtx := context.WithValue(ctx, ctxKey{}, nil)

	logger.LogAttrs(flushCtx, maxLevel, msg, finalAttrs...)
}
