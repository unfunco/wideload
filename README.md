# Wide Load

> To wide load, this yard bird tastes like fried toad turd.\
> – Love, Villain.

Wide Load is a Go library that provides a [slog] handler for accumulating log
records and attributes into a context-bound buffer. These accumulated logs can
later be emitted as a single "wide" log event, allowing for cleaner traces and
aggregated context, it is particularly useful in request-scoped workflows.

## Getting started

### Requirements

- [Go] 1.25+

### Examples

Some time in the future, the [examples] directory will contain several sample
programs demonstrating how to use Wide Load with different HTTP servers and
frameworks.

## License

© 2026 [Daniel Morris]\
Made available under the terms of the [MIT License].

[daniel morris]: https://unfun.co
[examples]: ./examples
[go]: https://go.dev
[mit license]: ./LICENSE.md
[slog]: https://pkg.go.dev/log/slog
