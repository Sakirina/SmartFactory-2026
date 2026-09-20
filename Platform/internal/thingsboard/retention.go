package thingsboard

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// PostgreSQL telemetry retention is applied explicitly, so dynamic policy changes
// use the same cutoff as the append-only observation archive.
func (a *Adapter) PruneTelemetry(ctx context.Context) error {
	docs, err := a.Store.List(ctx, "tb_mapping")
	if err != nil {
		return err
	}
	cutoff := a.Store.Now().AddDate(0, 0, -a.Store.Policy().Retention.RawDays).UnixMilli()
	count := 0
	for _, doc := range docs {
		mapping, err := store.Decode[Mapping](doc)
		if err != nil {
			return err
		}
		if mapping.Native.ID == "" {
			continue
		}
		base := "/api/plugins/telemetry/" + mapping.Native.EntityType + "/" + mapping.Native.ID
		keys := []string{}
		if err = a.Client.Do(ctx, "GET", base+"/keys/timeseries", nil, &keys); err != nil {
			return err
		}
		for start := 0; start < len(keys); start += 100 {
			end := start + 100
			if end > len(keys) {
				end = len(keys)
			}
			query := url.Values{"keys": {strings.Join(keys[start:end], ",")}, "startTs": {"0"}, "endTs": {strconv.FormatInt(cutoff-1, 10)}, "deleteLatest": {"false"}, "rewriteLatestIfDeleted": {"false"}}
			if err = a.Client.Do(ctx, "DELETE", base+"/timeseries/delete?"+query.Encode(), nil, nil); err != nil {
				return err
			}
			count += end - start
		}
	}
	return a.Store.Write(ctx, func(tx *store.Tx) error {
		return tx.Audit(model.Actor{UserID: "retention-worker", Source: a.Store.NodeID}, "retention.native_projection", "", "", map[string]any{"before_ms": cutoff, "series_processed": count, "raw_days": a.Store.Policy().Retention.RawDays})
	})
}
