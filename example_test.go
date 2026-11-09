package wideload_test

import (
	"context"
	"log/slog"
	"os"

	"github.com/unfunco/wideload"
)

func Example() {
	sink := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey || a.Key == "duration_ms" {
				return slog.Attr{}
			}
			return a
		},
	})
	handler := wideload.NewHandler(sink, nil)
	logger := slog.New(handler)

	ctx := wideload.NewContext(context.Background())
	wideload.Set(ctx, slog.String("request_id", "4f2a"))
	logger.DebugContext(ctx, "cache miss", "cache", "users")
	logger.InfoContext(ctx, "loaded", "user_id", 42, "plan", "free")
	logger.WarnContext(ctx, "slow query", "plan", "trial")
	_ = handler.Flush(ctx)

	// Output:
	// level=WARN msg="Wide event" request_id=4f2a cache=users user_id=42 plan=trial log.count=3 log.error_count=0
}
