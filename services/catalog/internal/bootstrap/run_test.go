package bootstrap

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestGracefulShutdownWaitsForRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
		case <-r.Context().Done():
			t.Error("request canceled before graceful completion")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, listener, time.Second, logger) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); _ = listener.Close() })
	client := &http.Client{Timeout: 3 * time.Second}
	responseDone := make(chan error, 1)
	go func() {
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != http.StatusNoContent {
				err = errors.New("unexpected response status")
			}
		}
		responseDone <- err
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("shutdown finished with in-flight request: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-responseDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("request did not finish")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server did not stop")
	}
}

func TestShutdownTimeoutCancelsRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	canceled := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	done := make(chan error, 1)
	go func() { done <- serve(ctx, server, listener, 20*time.Millisecond, logger) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); _ = listener.Close() })
	client := &http.Client{Timeout: 3 * time.Second}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		response, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not time out")
	}
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("request not canceled")
	}
	select {
	case <-requestDone:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not finish")
	}
}
