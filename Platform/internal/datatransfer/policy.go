package datatransfer

import (
	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/store"
	"context"
	"errors"
	"fmt"
)

func (b *Bridge) ApplyPolicy(ctx context.Context) error {
	doc, err := b.Store.Get(ctx, "parameter", "queue.watermarks")
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	parameter, err := store.Decode[struct {
		Version int64 `json:"version"`
	}](doc)
	if err != nil {
		return err
	}
	p := b.Store.Policy()
	response, err := b.Client.PushDeviceConfig(ctx, &dt.DeviceConfigUpdate{UpdateId: fmt.Sprintf("policy:%s:watermarks:%d", b.NodeID, parameter.Version), EntityRevision: parameter.Version, Action: dt.DeviceConfigUpdate_UPDATE_GLOBAL, Config: &dt.DeviceConfigUpdate_GlobalConfig{GlobalConfig: &dt.GlobalConfigPayload{Watermarks: &dt.QueueWatermarks{Reminder: float64(p.Reminder), Warning: float64(p.Warning), Error: float64(p.Error), Recovery: float64(p.Recovery)}}}})
	if err != nil {
		return err
	}
	if !response.Success {
		return fmt.Errorf("DataTransfer policy: %s", response.ErrorMessage)
	}
	if response.AppliedEntityRevision != parameter.Version {
		return fmt.Errorf("DataTransfer effective policy revision is %d; requested %d", response.AppliedEntityRevision, parameter.Version)
	}
	return nil
}
