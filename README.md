# Wide Load

> To wide load, this yard bird tastes like fried toad turd.\
> – Love, Villain.

Wide Load is a Go library that turns everything logged during a unit of work,
such as an HTTP request, into one wide event: a single structured [slog]
record carrying every field set along the way, emitted once when the work
ends. Searching one rich event per request beats stitching together dozens
of log lines, and keeping the debug detail attached to the events that fail
beats choosing a log level up front.

The handler wraps any `slog.Handler`. While a context carries a buffer, log
calls through it are captured instead of written:

- A later value for a key replaces an earlier one, so the event has one
  `user_id`, not three.
- Attributes bound with `Logger.With` and groups from `Logger.WithGroup` and
  `slog.Group` are kept and nested as the wrapped handler would nest them.
- Every level is captured whatever the wrapped handler's level, so the debug
  detail is there when the event turns out to be an error. `Options.Level`
  caps the capture if that is too much.
- Values are resolved on capture, so a `slog.LogValuer` reports what it held
  when it was logged.
- `Flush` emits the event once, at the highest level captured and at least
  Info, with `duration_ms` and a `log` group giving `count`, `error_count`
  and `first_error`. A second `Flush` does nothing, and records logged after
  it pass through to the wrapped handler.
- `Set` adds fields without a message, and `SetLevel` raises the event's
  level, so a handler that returns a 500 without logging still emits an
  error.
- `Middleware` gives each request its own buffer, records the method, path,
  pattern, status and response size under `http`, raises a 5xx or a panic to
  Error and flushes when the handler returns.

Records whose context carries no buffer pass straight through, so the handler
can be the process-wide default.

## Getting started

### Requirements

- [Go] 1.25+

### Usage

```go
sink := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
handler := wideload.NewHandler(sink, nil)
logger := slog.New(handler)

mux := http.NewServeMux()
mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	wideload.Set(ctx, slog.String("user_id", r.PathValue("id")))
	logger.DebugContext(ctx, "cache miss", "cache", "users")
	w.Write([]byte("hello"))
})

http.ListenAndServe(":8080", handler.Middleware(mux))
```

One request produces one line:

```json
{"time":"2026-11-09T09:30:00Z","level":"INFO","msg":"Wide event","http":{"method":"GET","path":"/users/7","pattern":"GET /users/{id}","status":200,"response_bytes":5},"user_id":"7","cache":"users","duration_ms":0.42,"log":{"count":1,"error_count":0}}
```

Outside HTTP, call `wideload.NewContext` at the start of the work and
`handler.Flush(ctx)` at the end.

### Examples

The [examples] directory holds sample programs. `go run ./examples/httpserver`
serves the request above on port 8080.

## License

© 2026 [Daniel Morris]\
Made available under the terms of the [MIT License].

[daniel morris]: https://unfun.co
[examples]: ./examples
[go]: https://go.dev
[mit license]: ./LICENSE.md
[slog]: https://pkg.go.dev/log/slog
