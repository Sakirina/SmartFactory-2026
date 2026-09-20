package app

import (
	"context"
	"time"
)

func (a *Application) applyPolicy(ctx context.Context) error {
	previous := a.Store.Policy()
	if err := a.Server.Config.ApplyPolicy(ctx); err != nil {
		return err
	}
	if a.Bridge != nil {
		call, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		if err := a.Bridge.ApplyPolicy(call); err != nil {
			a.Store.SetPolicy(previous)
			return err
		}
	}
	return nil
}
