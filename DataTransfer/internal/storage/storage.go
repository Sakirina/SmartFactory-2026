// Package storage 是本地缓冲存储的工厂入口,即信创适配的 StorageBackend
// 工程预留点(设计 DQ-008):国产数据库适配器未来在此边界接入,调用方不感知。
// 当前唯一实现为 SQLite;本包为遗留预留代码,保留勿删。
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
