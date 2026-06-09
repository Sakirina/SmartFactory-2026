package storage

import (
	"context"

	"competition2026/product/datatransfer/internal/buffer"
	"competition2026/product/datatransfer/internal/config"
)

type BufferStore interface {
	Close() error
}

func OpenBuffer(ctx context.Context, cfg config.BufferConfig) (*buffer.Store, error) {
	return buffer.Open(ctx, cfg)
}
