package service

import (
	"context"
	"encoding/json"
	"sync"
)

type sourceInfoContextKey struct{}

type sourceInfoResult struct {
	body json.RawMessage
	err  error
}

type sourceInfoSnapshot struct {
	mu      sync.Mutex
	workers map[*UserWorker]sourceInfoResult
}

// Discovery shares one private snapshot within a public info request. Nothing
// survives the request; mutations and later discovery always read fresh authority.
func withSourceInfoSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, sourceInfoContextKey{}, &sourceInfoSnapshot{
		workers: make(map[*UserWorker]sourceInfoResult),
	})
}

func sourceInfoBody(ctx context.Context, worker *UserWorker) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot, _ := ctx.Value(sourceInfoContextKey{}).(*sourceInfoSnapshot)
	if snapshot == nil {
		return worker.call(ctx, "/v1/info", nil)
	}
	snapshot.mu.Lock()
	defer snapshot.mu.Unlock()
	if result, ok := snapshot.workers[worker]; ok {
		return result.body, result.err
	}
	body, err := worker.call(ctx, "/v1/info", nil)
	snapshot.workers[worker] = sourceInfoResult{body: body, err: err}
	return body, err
}
