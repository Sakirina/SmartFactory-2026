package datatransfer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
	"google.golang.org/protobuf/encoding/protojson"
)

type ConnectorApplication struct {
	Reference       model.ConfigurationReference `json:"reference"`
	ApplyGeneration int64                        `json:"apply_generation"`
	ConsumerDigest  string                       `json:"consumer_digest"`
	AppliedUpdateID string                       `json:"applied_update_id"`
}

func (b *Bridge) ApplyConnectorConfiguration(ctx context.Context, c model.ConnectorConfiguration, raw json.RawMessage) error {
	return b.applyConnectorConfiguration(ctx, c, raw, false)
}
func (b *Bridge) ApplyReleaseConnectorConfiguration(ctx context.Context, c model.ConnectorConfiguration, raw json.RawMessage) error {
	return b.applyConnectorConfiguration(ctx, c, raw, true)
}
func (b *Bridge) applyConnectorConfiguration(ctx context.Context, c model.ConnectorConfiguration, raw json.RawMessage, release bool) error {
	if c.EdgeID != b.NodeID {
		return errors.New("connector configuration owner mismatch")
	}
	var update dt.DeviceConfigUpdate
	if err := protojson.Unmarshal(raw, &update); err != nil {
		return err
	}
	if update.GetConnectorConfig() == nil || update.GetConnectorConfig().ConnectorId != c.ConnectorID || update.GetConnectorConfig().Protocol != c.Protocol {
		return errors.New("connector configuration identity mismatch")
	}
	ref := model.ConfigurationReference{Kind: "connector", ID: c.ID, Version: c.Version, Digest: store.Hash(c)}
	return b.Store.Write(ctx, func(tx *store.Tx) error {
		if pin, e := tx.Get("configuration_local_release_target", "connector:"+c.ID); e == nil {
			var target model.ConfigurationReference
			if e = store.DecodeJSON(pin.Data, &target); e != nil {
				return e
			}
			if target != ref {
				return fmt.Errorf("%w: connector is fixed by an active release", store.ErrConflict)
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		} else if release {
			return errors.New("explicit local release target required")
		}
		var previous ConnectorApplication
		if d, e := tx.Get("connector_runtime_application", c.ID); e == nil {
			if e = store.DecodeJSON(d.Data, &previous); e != nil {
				return e
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if !release && previous.Reference.Version > c.Version {
			return store.ErrConflict
		}
		if !release && previous.Reference == ref {
			actual, e := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: c.ConnectorID})
			if e != nil {
				return e
			}
			if actual.Found && actual.AppliedEntityRevision == previous.ApplyGeneration && actual.PublicConfigurationSha256 == previous.ConsumerDigest && actual.AppliedUpdateId == previous.AppliedUpdateID {
				return nil
			}
		}
		generation := c.Version
		if generation <= previous.ApplyGeneration {
			generation = previous.ApplyGeneration + 1
		}
		before, e := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: c.ConnectorID})
		if e != nil {
			return e
		}
		if generation <= before.GetAppliedEntityRevision() {
			generation = before.GetAppliedEntityRevision() + 1
		}
		update.UpdateId = fmt.Sprintf("connector-application:%s:%d:%s", c.ID, generation, ref.Digest)
		update.EntityRevision = generation
		update.ChangeSource = "registered-edge-configuration"
		if release {
			update.ChangeSource = "authorized-fixed-release"
		}
		response, e := b.Client.PushDeviceConfig(ctx, &update)
		if e != nil {
			return e
		}
		if !response.Success {
			return errors.New("DataTransfer rejected the connector configuration")
		}
		if response.AppliedEntityRevision != generation {
			return errors.New("DataTransfer configuration confirmation generation mismatch")
		}
		actual, e := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: c.ConnectorID, ExpectedConfiguration: update.GetConnectorConfig()})
		if e != nil {
			return e
		}
		if !actual.Found || actual.AppliedEntityRevision != generation || len(actual.PublicConfigurationSha256) != 64 || actual.AppliedUpdateId != update.UpdateId || !actual.MatchesExpected {
			return errors.New("DataTransfer configuration consumer differs from confirmation")
		}
		return tx.SetEphemeral("connector_runtime_application", c.ID, ConnectorApplication{Reference: ref, ApplyGeneration: generation, ConsumerDigest: actual.PublicConfigurationSha256, AppliedUpdateID: actual.AppliedUpdateId})
	})
}

func (b *Bridge) ReadConnectorRuntime(ctx context.Context, ref model.ConfigurationReference) (int64, string, error) {
	d, err := b.Store.Get(ctx, "connector_runtime_application", ref.ID)
	if err != nil {
		return 0, "", err
	}
	var saved ConnectorApplication
	if err = store.DecodeJSON(d.Data, &saved); err != nil {
		return 0, "", err
	}
	if saved.Reference != ref {
		return 0, "", errors.New("connector source reference differs from runtime")
	}
	parts := stringsLast(ref.ID)
	actual, err := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: parts})
	if err != nil {
		return 0, "", err
	}
	if !actual.Found || actual.AppliedEntityRevision != saved.ApplyGeneration || actual.PublicConfigurationSha256 != saved.ConsumerDigest || actual.AppliedUpdateId != saved.AppliedUpdateID {
		return actual.GetAppliedEntityRevision(), actual.GetPublicConfigurationSha256(), errors.New("connector consumer drift detected")
	}
	return saved.ApplyGeneration, saved.ConsumerDigest, nil
}
func stringsLast(id string) string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '/' {
			return id[i+1:]
		}
	}
	return id
}

// RemoveReleaseConnectorConfiguration removes a previous release-owned
// consumer while retaining its monotonic application generation.
func (b *Bridge) RemoveReleaseConnectorConfiguration(ctx context.Context, ref model.ConfigurationReference) error {
	return b.Store.Write(ctx, func(tx *store.Tx) error {
		d, e := tx.Get("connector_runtime_application", ref.ID)
		if errors.Is(e, store.ErrNotFound) {
			return nil
		}
		if e != nil {
			return e
		}
		var saved ConnectorApplication
		if e = store.DecodeJSON(d.Data, &saved); e != nil {
			return e
		}
		if saved.Reference != ref {
			return nil
		}
		actual, e := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: stringsLast(ref.ID)})
		if e != nil {
			return e
		}
		if !actual.Found {
			return nil
		}
		if actual.AppliedEntityRevision != saved.ApplyGeneration || actual.AppliedUpdateId != saved.AppliedUpdateID || actual.PublicConfigurationSha256 != saved.ConsumerDigest {
			return errors.New("connector consumer drift detected before release removal")
		}
		generation := actual.AppliedEntityRevision + 1
		updateID := fmt.Sprintf("connector-release-removal:%s:%d:%s", ref.ID, generation, ref.Digest)
		update := &dt.DeviceConfigUpdate{UpdateId: updateID, EntityRevision: generation, ChangeSource: "authorized-fixed-release-removal", Action: dt.DeviceConfigUpdate_REMOVE_CONNECTOR, Config: &dt.DeviceConfigUpdate_ConnectorConfig{ConnectorConfig: &dt.ConnectorConfigPayload{ConnectorId: stringsLast(ref.ID)}}}
		response, e := b.Client.PushDeviceConfig(ctx, update)
		if e != nil {
			return e
		}
		if !response.Success || response.AppliedEntityRevision != generation {
			return errors.New("DataTransfer release removal confirmation differs")
		}
		removed, e := b.Client.GetConnectorConfiguration(ctx, &dt.ConnectorConfigurationRequest{ConnectorId: stringsLast(ref.ID)})
		if e != nil {
			return e
		}
		if removed.Found || removed.AppliedEntityRevision != generation || removed.AppliedUpdateId != updateID {
			return errors.New("DataTransfer release consumer was not removed")
		}
		return tx.SetEphemeral("connector_runtime_application", ref.ID, ConnectorApplication{Reference: model.ConfigurationReference{Kind: "connector", ID: ref.ID}, ApplyGeneration: generation, AppliedUpdateID: updateID})
	})
}
