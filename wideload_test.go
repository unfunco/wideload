package wideload_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/unfunco/wideload"
)

func TestWideHandler(t *testing.T) {
	var out bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&out, nil)

	wh := wideload.NewHandler(jsonHandler)
	logger := slog.New(wh)

	ctx := context.Background()
	ctx = wideload.NewContext(ctx)

	logger.InfoContext(ctx, "Step 1", slog.String("key1", "value1"))
	logger.WarnContext(ctx, "Step 2", slog.Int("count", 42))

	if out.Len() > 0 {
		t.Fatalf("Expected no output before Flush, got: %s", out.String())
	}

	wideload.Flush(ctx, logger)

	output := out.String()
	t.Logf("Output: %s", output)

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if result["level"] != "WARN" {
		t.Errorf("Expected level WARN, got %v", result["level"])
	}

	if result["msg"] != "Step 2" {
		t.Errorf("Expected msg 'Step 2', got %v", result["msg"])
	}

	if result["key1"] != "value1" {
		t.Errorf("Expected key1=value1, got %v", result["key1"])
	}
	if val, ok := result["count"].(float64); !ok || val != 42 {
		t.Errorf("Expected count=42, got %v", result["count"])
	}

	logs, ok := result["logs"].([]any)
	if !ok {
		t.Fatalf("Expected 'logs' array, got %T", result["logs"])
	}
	if len(logs) != 2 {
		t.Errorf("Expected 2 collected logs, got %d", len(logs))
	}
	if logs[0] != "Step 1" || logs[1] != "Step 2" {
		t.Errorf("Unexpected logs content: %v", logs)
	}
}

func TestDebugOnlyLevel(t *testing.T) {
	var out bytes.Buffer
	opts := &slog.HandlerOptions{Level: slog.LevelDebug}
	jsonHandler := slog.NewJSONHandler(&out, opts)
	wideHandler := wideload.NewHandler(jsonHandler)
	logger := slog.New(wideHandler)

	ctx := context.Background()
	ctx = wideload.NewContext(ctx)

	logger.DebugContext(ctx, "Debug message")

	wideload.Flush(ctx, logger)

	var result map[string]any
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if result["level"] != "DEBUG" {
		t.Errorf("Expected level DEBUG, got %v", result["level"])
	}
}

func TestNoContextPassThrough(t *testing.T) {
	var out bytes.Buffer
	jsonHandler := slog.NewJSONHandler(&out, nil)
	wideHandler := wideload.NewHandler(jsonHandler)
	logger := slog.New(wideHandler)

	logger.Info("Direct message", "foo", "bar")

	if out.Len() == 0 {
		t.Fatal("Expected immediate output when no context is active")
	}

	var result map[string]any
	_ = json.Unmarshal(out.Bytes(), &result)
	if result["msg"] != "Direct message" {
		t.Errorf("Expected direct message, got %v", result["msg"])
	}
}
