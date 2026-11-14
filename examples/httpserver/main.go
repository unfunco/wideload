// Command httpserver serves two routes and emits one wide event per request.
package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/unfunco/wideload"
)

func main() {
	sink := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	handler := wideload.NewHandler(sink, nil)
	logger := slog.New(handler)
	slog.SetDefault(logger)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		wideload.Set(ctx, slog.String("user_id", r.PathValue("id")))
		logger.DebugContext(ctx, "cache miss", "cache", "users")
		if r.PathValue("id") == "0" {
			logger.ErrorContext(ctx, "user not found")
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte("hello\n"))
	})

	logger.Info("listening", "addr", ":8080")
	if err := http.ListenAndServe(":8080", handler.Middleware(mux)); err != nil {
		logger.Error("server stopped", "err", err)
		os.Exit(1)
	}
}
