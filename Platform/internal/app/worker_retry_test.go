package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerBacksOffRemoteFailureAndCancelsPromptly(t *testing.T) {
	a := &Application{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	first := make(chan struct{})
	a.worker(ctx, time.Millisecond, func(context.Context) error {
		if calls.Add(1) == 1 {
			close(first)
		}
		return errors.New("remote offline")
	})
	<-first
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatal("failed queue was retried at the normal polling rate", calls.Load())
	}
	cancel()
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("backoff delayed shutdown")
	}
}
