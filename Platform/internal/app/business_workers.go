package app

import (
	"context"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (a *Application) startBusinessWorkers(ctx context.Context) {
	if a.Native != nil {
		a.worker(ctx, 5*time.Second, func(ctx context.Context) error {
			return a.Native.PollAlarmStates(ctx, a.Server.BusinessApplication().UpdateNativeAlarm)
		})
	}
	if a.Bridge != nil {
		a.worker(ctx, time.Second, func(ctx context.Context) error {
			return a.consume(ctx, "connector_config", 16, func(d store.Delivery) error {
				var c model.ConnectorConfiguration
				if e := store.DecodeJSON(d.Payload, &c); e != nil {
					return e
				}
				return a.Server.BusinessApplication().ApplyConnectorConfiguration(ctx, c, a.Bridge)
			})
		})
	}
}
