package app

import (
	"competition2026/product/platform/internal/store"
	"context"
	"errors"
	"time"
)

func (a *Application) retention(ctx context.Context) error {
	now := a.Store.Now()
	end := now.Truncate(time.Hour).UnixMilli()
	start := end - int64(time.Hour/time.Millisecond)
	if d, e := a.Store.Get(ctx, "retention_cursor", "rollup"); e == nil {
		var v struct {
			NextMS int64 `json:"next_ms"`
		}
		if e = store.DecodeJSON(d.Data, &v); e != nil {
			return e
		}
		if v.NextMS < start {
			start = v.NextMS
		}
	} else if errors.Is(e, store.ErrNotFound) {
		first, _, err := a.Store.ObservationRange(ctx, nil, false)
		if err != nil {
			return err
		}
		if first > 0 && first < start {
			start = first / 60000 * 60000
		}

	} else {
		return e
	}
	cut := now.AddDate(0, 0, -a.Store.Policy().Retention.RawDays).UnixMilli()
	if start < cut {
		start = cut
	}
	for start < end {
		to := start + int64(time.Hour/time.Millisecond)
		if to > end {
			to = end
		}
		if e := a.Store.BuildRollups(ctx, start, to); e != nil {
			return e
		}
		if e := a.Store.Write(ctx, func(t *store.Tx) error {
			return t.SetEphemeral("retention_cursor", "rollup", map[string]any{"next_ms": to})
		}); e != nil {
			return e
		}
		start = to
	}
	if _, err := a.Store.ApplyRetention(ctx, a.Store.Policy().Retention); err != nil {
		return err
	}
	if a.Native != nil {
		return a.Native.PruneTelemetry(ctx)
	}
	return nil
}
