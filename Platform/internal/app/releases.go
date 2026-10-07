package app

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"competition2026/product/platform/internal/releaseruntime"
	"competition2026/product/platform/pkg/model"
)

func (a *Application) initializeRelease(ctx context.Context) error {
	if a.Options.ReleaseArtifactRoot == "" {
		a.Options.ReleaseArtifactRoot = filepath.Join(filepath.Dir(a.Options.KeyFile), "release-artifacts")
	}
	a.Server.ReleaseArtifactRoot = a.Options.ReleaseArtifactRoot
	if a.Options.ReleasePayload == "" {
		return nil
	}
	var connector func(context.Context, model.ConnectorConfiguration, json.RawMessage) error
	if a.Bridge != nil {
		connector = a.Bridge.ApplyReleaseConnectorConfiguration
	}
	runtime, err := releaseruntime.Open(ctx, a.Store, a.Server.Config, a.Options.Mode, a.Options.NodeID, a.Options.ReleasePayload, connector)
	if err != nil {
		return err
	}
	a.Server.ReleaseRuntime = runtime
	return nil
}

func (a *Application) startReleaseCoordinator(ctx context.Context) {
	if a.Options.Mode == "cloud" {
		a.worker(ctx, 500*time.Millisecond, a.Server.ReleaseApplication().Reconcile)
	}
}
