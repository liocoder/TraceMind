package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liocoder/TraceMind/internal/config"
)

func newTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()

	client, err := NewClient(config.LLMConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Model:   "test-model",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return client
}

func chatResponseBody(content string) string {
	data, _ := json.Marshal(map[string]any{
		"choices": []map[string]any{
			{
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
	})
	return string(data)
}

func TestCompleteRetriesOn429(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte("rate limited"))
			return
		}
		_, _ = w.Write([]byte(chatResponseBody("ok")))
	}))
	defer server.Close()

	client := newTestClient(t, server)

	completion, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", attempts)
	}
	if completion.Message.Content != "ok" {
		t.Fatalf("unexpected content %q", completion.Message.Content)
	}
}

func TestCompleteRetriesOn500(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(chatResponseBody("ok")))
	}))
	defer server.Close()

	client := newTestClient(t, server)

	if _, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestCompleteNoRetryOn401(t *testing.T) {
	attempts := 0

	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		attempts++
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("invalid api key"))
	}))
	defer server.Close()

	client := newTestClient(t, server)

	_, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err == nil {
		t.Fatal("expected an error for 401")
	}
	if attempts != 1 {
		t.Fatalf("expected no retry on 401, got %d attempts", attempts)
	}
	if !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestCompleteRetriesNetworkError(t *testing.T) {
	// 第一次请求直接中断连接（模拟网络层失败），重试后正常应答。
	attempts := 0
	switching := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		attempts++
		if attempts == 1 {
			hj, ok := w.(http.Hijacker)
			if ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
		}
		_, _ = w.Write([]byte(chatResponseBody("ok")))
	}))
	defer switching.Close()

	client := newTestClient(t, switching)

	completion, err := client.Complete(context.Background(), []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completion.Message.Content != "ok" {
		t.Fatalf("unexpected content %q", completion.Message.Content)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestCompleteRespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		// 一直 429，且放慢响应，让重试循环被 ctx 打断。
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("rate limited"))
	}))
	defer server.Close()

	client := newTestClient(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := client.Complete(ctx, []Message{
		{Role: "user", Content: "hi"},
	}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("retry loop ignored context cancellation, took %v", elapsed)
	}
}

func TestIsRetryableError(t *testing.T) {
	if !isRetryableError(&apiError{StatusCode: 429}) {
		t.Fatal("429 should be retryable")
	}
	if !isRetryableError(&apiError{StatusCode: 503}) {
		t.Fatal("503 should be retryable")
	}
	if isRetryableError(&apiError{StatusCode: 401}) {
		t.Fatal("401 should not be retryable")
	}
	if !isRetryableError(&transportError{err: fmt.Errorf("boom")}) {
		t.Fatal("transport error should be retryable")
	}
	if isRetryableError(fmt.Errorf("decode response: boom")) {
		t.Fatal("plain decode error should not be retryable")
	}
}
