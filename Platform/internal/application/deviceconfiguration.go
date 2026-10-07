package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type SaveDeviceConfigurationInput struct {
	ID              string                  `json:"id" required:"true"`
	RequestID       string                  `json:"request_id" required:"true"`
	ExpectedVersion int64                   `json:"expected_version" minimum:"1" required:"true"`
	Parameters      deviceconfig.Parameters `json:"parameters" required:"true"`
}

func (s *Business) DeviceProtocols(ctx context.Context, p identity.Principal) ([]deviceconfig.ProtocolMetadata, error) {
	if _, err := s.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	return deviceconfig.Catalog(), nil
}
func (s *Business) ValidateDeviceConfiguration(ctx context.Context, p identity.Principal, input deviceconfig.Request) (deviceconfig.Result, error) {
	resources := []string{}
	if input.DeviceID != "" {
		resources = append(resources, input.DeviceID)
	}
	if _, err := s.authorize(ctx, p, "read", resources); err != nil {
		return deviceconfig.Result{}, err
	}
	return deviceconfig.Validate(input)
}

type DeviceConfigurationDetail struct {
	Entity         model.Entity           `json:"entity"`
	Configuration  deviceconfig.Result    `json:"configuration"`
	AllowedActions []model.BusinessAction `json:"allowed_actions"`
}
type SaveConnectorConfigurationInput struct {
	RequestID       string                  `json:"request_id" required:"true"`
	ExpectedVersion int64                   `json:"expected_version" minimum:"0" required:"true"`
	GroupID         string                  `json:"group_id" required:"true"`
	EdgeID          string                  `json:"edge_id" required:"true"`
	Protocol        string                  `json:"protocol" required:"true"`
	Parameters      deviceconfig.Parameters `json:"parameters" required:"true"`
}

func (s *Business) DeviceConfiguration(ctx context.Context, p identity.Principal, id string) (DeviceConfigurationDetail, error) {
	seen, err := s.authorize(ctx, p, "read", []string{id})
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	doc, err := s.read(ctx, "entity", id, seen)
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	e, err := store.Decode[model.Entity](doc)
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	if e.Kind != "device" {
		return DeviceConfigurationDetail{}, errors.New("device entity required")
	}
	request := deviceconfig.Request{Protocol: e.Protocol, DeviceID: e.ID, Config: e.Config}
	if len(e.Config) == 0 {
		request.Parameters = &deviceconfig.Parameters{Kind: "device", DeviceID: e.ID, DeviceName: e.Name}
	}
	configuration, err := deviceconfig.Validate(request)
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	action := model.BusinessAction{Action: "save", Allowed: true}
	if err = s.Identity.Permit(ctx, p, "register", id); err != nil {
		action.Allowed = false
		action.Reason = err.Error()
	} else if s.Mode != "edge" || e.EdgeID != s.NodeID {
		action.Allowed = false
		action.Reason = "physical device configuration is maintained by its owning edge"
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return DeviceConfigurationDetail{Entity: e, Configuration: configuration, AllowedActions: []model.BusinessAction{action}}, err
}

func (s *Business) SaveDeviceConfiguration(ctx context.Context, p identity.Principal, input SaveDeviceConfigurationInput) (DeviceConfigurationDetail, error) {
	seen, err := s.authorize(ctx, p, "register", []string{input.ID})
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	doc, err := s.read(ctx, "entity", input.ID, seen)
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	e, err := store.Decode[model.Entity](doc)
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	if e.Kind != "device" || s.Mode != "edge" || e.EdgeID != s.NodeID {
		return DeviceConfigurationDetail{}, identity.ErrDenied
	}
	if err = s.access().permit(ctx, p, "register", e.EdgeID, seen); err != nil {
		return DeviceConfigurationDetail{}, err
	}
	validated, err := deviceconfig.Validate(deviceconfig.Request{Protocol: e.Protocol, DeviceID: e.ID, Config: e.Config, Parameters: &input.Parameters})
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	if !validated.Valid {
		return DeviceConfigurationDetail{}, fmt.Errorf("device configuration %s: %s", validated.Issues[0].Path, validated.Issues[0].Message)
	}
	if validated.Parameters.Kind != "device" {
		return DeviceConfigurationDetail{}, errors.New("device payload required")
	}
	connectorID := e.EdgeID + "/" + validated.Parameters.ConnectorID
	if cdoc, e2 := s.Store.Get(ctx, "connector_configuration", connectorID); e2 == nil {
		c, e2 := store.Decode[model.ConnectorConfiguration](cdoc)
		if e2 != nil {
			return DeviceConfigurationDetail{}, e2
		}
		if c.Protocol != validated.Protocol {
			return DeviceConfigurationDetail{}, errors.New("device protocol differs from registered connector")
		}
		if e2 = seen.remember("connector_configuration", connectorID, cdoc.Version); e2 != nil {
			return DeviceConfigurationDetail{}, e2
		}
	} else if !errors.Is(e2, store.ErrNotFound) {
		return DeviceConfigurationDetail{}, e2
	}
	scope := "device-configuration:" + input.ID + ":" + p.User.ID
	hash := store.Hash(input)
	var result model.Entity
	e.Config = validated.Config
	e.Version = input.ExpectedVersion + 1
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, err := tx.BusinessRequest(scope, input.RequestID, hash, &result)
		if err != nil || duplicate {
			return err
		}
		if doc.Version != input.ExpectedVersion {
			return store.ErrConflict
		}
		if err = seen.check(tx); err != nil {
			return err
		}
		if _, err = tx.Put("entity", e.ID, input.ExpectedVersion, e); err != nil {
			return err
		}
		if err = tx.Enqueue(fmt.Sprintf("device-config:%s:%d", e.ID, e.Version), "device_config", e.ID, e); err != nil {
			return err
		}
		if err = tx.Enqueue(fmt.Sprintf("entity:%s:%d", e.ID, e.Version), "tb_entity", e.ID, e); err != nil {
			return err
		}
		if err = tx.Audit(p.Actor, "device.configuration", e.ID, input.RequestID, map[string]any{"version": e.Version, "connector_id": validated.Parameters.ConnectorID, "protocol": validated.Protocol}); err != nil {
			return err
		}
		result = e
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, e)
	})
	if err != nil {
		return DeviceConfigurationDetail{}, err
	}
	return s.DeviceConfiguration(ctx, p, e.ID)
}

func (s *Business) ConnectorConfiguration(ctx context.Context, p identity.Principal, id string) (model.ConnectorConfigurationDetail, error) {
	seen, err := s.authorize(ctx, p, "read", nil)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	doc, err := s.read(ctx, "connector_configuration", id, seen)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	c, err := store.Decode[model.ConnectorConfiguration](doc)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	for _, resource := range []string{c.GroupID, c.EdgeID} {
		if err = s.access().permit(ctx, p, "read", resource, seen); err != nil {
			return model.ConnectorConfigurationDetail{}, err
		}
	}
	result := model.ConnectorConfigurationDetail{Configuration: c, Receipt: model.ConnectorConfigurationReceipt{ConfigurationID: id, ConfigurationVersion: c.Version, SourceID: c.EdgeID, Status: "pending"}, AllowedActions: []model.BusinessAction{}}
	if d, e := s.Store.Get(ctx, "connector_configuration_receipt", id); e == nil {
		result.Receipt, e = store.Decode[model.ConnectorConfigurationReceipt](d)
		if e != nil {
			return result, e
		}
		if result.Receipt.ConfigurationVersion != c.Version {
			result.Receipt.Status = "pending"
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return result, e
	}
	action := model.BusinessAction{Action: "save", Allowed: true}
	for _, resource := range []string{c.GroupID, c.EdgeID} {
		if e := s.Identity.Permit(ctx, p, "register", resource); e != nil {
			action.Allowed = false
			action.Reason = e.Error()
		}
	}
	if s.Mode != "edge" || c.EdgeID != s.NodeID {
		action.Allowed = false
		action.Reason = "connector configuration is maintained by its owning edge"
	}
	result.AllowedActions = append(result.AllowedActions, action)
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return result, err
}

func (s *Business) ConnectorConfigurations(ctx context.Context, p identity.Principal) ([]model.ConnectorConfigurationDetail, error) {
	if _, err := s.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	docs, err := s.Store.List(ctx, "connector_configuration")
	if err != nil {
		return nil, err
	}
	result := []model.ConnectorConfigurationDetail{}
	for _, d := range docs {
		detail, e := s.ConnectorConfiguration(ctx, p, d.ID)
		if errors.Is(e, identity.ErrDenied) {
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, detail)
	}
	return result, nil
}

func (s *Business) SaveConnectorConfiguration(ctx context.Context, p identity.Principal, input SaveConnectorConfigurationInput) (model.ConnectorConfigurationDetail, error) {
	var result model.ConnectorConfiguration
	if input.ExpectedVersion < 0 || input.Parameters.Kind != "connector" {
		return model.ConnectorConfigurationDetail{}, errors.New("connector parameters and a nonnegative expected version are required")
	}
	seen, err := s.authorize(ctx, p, "register", []string{input.EdgeID, input.GroupID})
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	if s.Mode != "edge" || input.EdgeID != s.NodeID {
		return model.ConnectorConfigurationDetail{}, identity.ErrDenied
	}
	edoc, err := s.read(ctx, "entity", input.EdgeID, seen)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	edge, err := store.Decode[model.Entity](edoc)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	if edge.Kind != "edge" || edge.Status != "active" {
		return model.ConnectorConfigurationDetail{}, errors.New("target edge is not active")
	}
	group, err := s.read(ctx, "entity", input.GroupID, seen)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	asset, err := store.Decode[model.Entity](group)
	if err != nil || asset.Kind != "asset" {
		return model.ConnectorConfigurationDetail{}, errors.New("connector group must be a current asset")
	}
	id := input.EdgeID + "/" + input.Parameters.ConnectorID
	request := deviceconfig.Request{Protocol: input.Protocol, Parameters: &input.Parameters}
	if previous, e := s.Store.Get(ctx, "connector_configuration", id); e == nil {
		old, e := store.Decode[model.ConnectorConfiguration](previous)
		if e != nil {
			return model.ConnectorConfigurationDetail{}, e
		}
		if old.GroupID != input.GroupID || old.EdgeID != input.EdgeID {
			return model.ConnectorConfigurationDetail{}, errors.New("connector ownership cannot change")
		}
		if err = seen.remember("connector_configuration", id, previous.Version); err != nil {
			return model.ConnectorConfigurationDetail{}, err
		}
		request.Config = old.Config
		if old.CredentialRef != "" {
			if secret, e := s.Store.Get(ctx, "connector_configuration_secret", old.CredentialRef); e == nil {
				var value struct {
					Ciphertext      string `json:"ciphertext"`
					ConfigurationID string `json:"configuration_id"`
					Version         int64  `json:"version"`
				}
				if e = store.DecodeJSON(secret.Data, &value); e != nil {
					return model.ConnectorConfigurationDetail{}, e
				}
				if value.ConfigurationID != old.ID || value.Version != old.Version {
					return model.ConnectorConfigurationDetail{}, store.ErrConflict
				}
				raw, e := s.Identity.Decrypt("connector:"+old.CredentialRef, value.Ciphertext)
				if e != nil {
					return model.ConnectorConfigurationDetail{}, e
				}
				request.Config = json.RawMessage(raw)
			} else {
				return model.ConnectorConfigurationDetail{}, e
			}
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return model.ConnectorConfigurationDetail{}, e
	}
	validated, err := deviceconfig.Validate(request)
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	if !validated.Valid {
		return model.ConnectorConfigurationDetail{}, fmt.Errorf("connector configuration %s: %s", validated.Issues[0].Path, validated.Issues[0].Message)
	}
	secretRef := ""
	ciphertext := ""
	public := validated
	if _, present := validated.Parameters.Connection["password"]; present {
		secretRef = model.ConnectorSecretReference(id, input.ExpectedVersion+1)
		ciphertext, err = s.Identity.Encrypt("connector:"+secretRef, string(validated.Config))
		if err != nil {
			return model.ConnectorConfigurationDetail{}, err
		}
		public, err = deviceconfig.PublicConfiguration(validated)
		if err != nil {
			return model.ConnectorConfigurationDetail{}, err
		}
	}
	now := s.Store.CurrentTime().UnixMilli()
	result = model.ConnectorConfiguration{ID: id, ConnectorID: input.Parameters.ConnectorID, EdgeID: input.EdgeID, GroupID: input.GroupID, Protocol: validated.Protocol, Config: public.Config, CredentialRef: secretRef, Version: input.ExpectedVersion + 1, UpdatedMS: now, Actor: p.Actor}
	scope := "connector-configuration:" + id + ":" + p.User.ID
	hash := store.Hash(input)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, input.RequestID, hash, &result)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		if ciphertext != "" {
			if _, e = tx.Put("connector_configuration_secret", secretRef, 0, map[string]any{"ciphertext": ciphertext, "configuration_id": id, "version": result.Version}); e != nil {
				return e
			}
		}
		if _, e = tx.Put("connector_configuration", id, input.ExpectedVersion, result); e != nil {
			return e
		}
		if _, e = tx.Put("connector_configuration_version", model.ConnectorSecretReference(id, result.Version), 0, result); e != nil {
			return e
		}
		if e = tx.Enqueue(fmt.Sprintf("connector-config:%s:%d", id, result.Version), "connector_config", input.EdgeID, result); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "connector.configuration", input.GroupID, input.RequestID, map[string]any{"configuration": result.ID, "version": result.Version, "edge_id": result.EdgeID, "credentials_referenced": secretRef != ""}); e != nil {
			return e
		}
		return tx.CompleteBusinessRequest(scope, input.RequestID, hash, result)
	})
	if err != nil {
		return model.ConnectorConfigurationDetail{}, err
	}
	return s.ConnectorConfiguration(ctx, p, id)
}

type ConnectorApplier interface {
	ApplyConnectorConfiguration(context.Context, model.ConnectorConfiguration, json.RawMessage) error
}

func (s *Business) ApplyConnectorConfiguration(ctx context.Context, configuration model.ConnectorConfiguration, applier ConnectorApplier) error {
	if configuration.EdgeID != s.NodeID || s.Mode != "edge" {
		return identity.ErrDenied
	}
	doc, err := s.Store.Get(ctx, "connector_configuration", configuration.ID)
	if err != nil {
		return err
	}
	current, err := store.Decode[model.ConnectorConfiguration](doc)
	if err != nil {
		return err
	}
	if current.Version != configuration.Version {
		return nil
	}
	if store.Hash(current) != store.Hash(configuration) {
		return store.ErrConflict
	}
	if old, e := s.Store.Get(ctx, "connector_configuration_receipt", configuration.ID); e == nil {
		receipt, e := store.Decode[model.ConnectorConfigurationReceipt](old)
		if e != nil {
			return e
		}
		if receipt.ConfigurationVersion == configuration.Version && receipt.Status == "applied" {
			return nil
		}
	}
	payload := configuration.Config
	if configuration.CredentialRef != "" {
		secret, e := s.Store.Get(ctx, "connector_configuration_secret", configuration.CredentialRef)
		if e != nil {
			return e
		}
		var value struct {
			Ciphertext      string `json:"ciphertext"`
			ConfigurationID string `json:"configuration_id"`
			Version         int64  `json:"version"`
		}
		if e = store.DecodeJSON(secret.Data, &value); e != nil {
			return e
		}
		if value.ConfigurationID != configuration.ID || value.Version != configuration.Version || configuration.CredentialRef != model.ConnectorSecretReference(configuration.ID, configuration.Version) {
			return store.ErrConflict
		}
		raw, e := s.Identity.Decrypt("connector:"+configuration.CredentialRef, value.Ciphertext)
		if e != nil {
			return e
		}
		payload = json.RawMessage(raw)
	}
	validated, e := deviceconfig.Validate(deviceconfig.Request{Protocol: configuration.Protocol, Config: payload})
	if e != nil {
		return e
	}
	public, e := deviceconfig.PublicConfiguration(validated)
	if e != nil {
		return e
	}
	var actual, expected any
	if e = store.DecodeJSON(public.Config, &actual); e != nil {
		return e
	}
	if e = store.DecodeJSON(configuration.Config, &expected); e != nil {
		return e
	}
	if store.Hash(actual) != store.Hash(expected) {
		return store.ErrConflict
	}
	applyErr := applier.ApplyConnectorConfiguration(ctx, configuration, payload)
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		d, e := tx.Get("connector_configuration", configuration.ID)
		if e != nil {
			return e
		}
		if d.Version != doc.Version {
			return nil
		}
		old, e := tx.Get("connector_configuration_receipt", configuration.ID)
		if e != nil && !errors.Is(e, store.ErrNotFound) {
			return e
		}
		r := model.ConnectorConfigurationReceipt{ID: configuration.ID, ConfigurationID: configuration.ID, ConfigurationVersion: configuration.Version, SourceID: s.NodeID, Status: "applied", AtMS: s.Store.CurrentTime().UnixMilli(), Version: old.Version + 1}
		if applyErr != nil {
			r.Status = "failed"
			r.Reason = "DataTransfer rejected or could not confirm the configuration"
		}
		if _, e = tx.Put("connector_configuration_receipt", r.ID, old.Version, r); e != nil {
			return e
		}
		return tx.Audit(model.Actor{UserID: "configuration-worker", Source: s.NodeID}, "connector.configuration."+r.Status, configuration.GroupID, configuration.ID, r)
	})
	if err != nil {
		return err
	}
	return applyErr
}
