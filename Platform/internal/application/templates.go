package application

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/internal/application/definitioncommit"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/scenetemplates"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type RetryTemplateBatchInput struct {
	RequestID       string `json:"request_id" required:"true"`
	ExpectedVersion int64  `json:"expected_version" minimum:"1" required:"true"`
}

func (s *Business) Templates(ctx context.Context, p identity.Principal) ([]scenetemplates.Template, error) {
	if _, err := s.authorize(ctx, p, "read", nil); err != nil {
		return nil, err
	}
	return scenetemplates.Catalog(), nil
}

func (s *Business) TemplateBatch(ctx context.Context, p identity.Principal, id string) (model.TemplateBatch, error) {
	seen, err := s.authorize(ctx, p, "read", nil)
	if err != nil {
		return model.TemplateBatch{}, err
	}
	doc, err := s.read(ctx, "template_batch", id, seen)
	if err != nil {
		return model.TemplateBatch{}, err
	}
	batch, err := store.Decode[model.TemplateBatch](doc)
	if err != nil {
		return batch, err
	}
	if err = s.access().permit(ctx, p, "read", batch.GroupID, seen); err != nil {
		return model.TemplateBatch{}, err
	}
	for _, binding := range batch.Bindings {
		if err = s.access().permit(ctx, p, "read", binding.DeviceID, seen); err != nil {
			return model.TemplateBatch{}, err
		}
	}
	if batch.CreatedBy != p.User.ID {
		for _, instance := range batch.Input.Instances {
			if err = s.access().permit(ctx, p, "read", instance.DeviceID, seen); err != nil {
				return model.TemplateBatch{}, err
			}
		}
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) })
	return batch, err
}

func (s *Business) PrepareTemplateBatch(ctx context.Context, p identity.Principal, input model.TemplateBatchInput) (model.TemplateBatch, error) {
	if err := businessID(input.ID); err != nil {
		return model.TemplateBatch{}, err
	}
	if err := businessID(input.RequestID); err != nil {
		return model.TemplateBatch{}, err
	}
	if len(input.Instances) < 1 || len(input.Instances) > 20 {
		return model.TemplateBatch{}, errors.New("a template batch requires 1 to 20 instances")
	}
	seen, err := s.authorize(ctx, p, "draft", []string{input.GroupID})
	if err != nil {
		return model.TemplateBatch{}, err
	}
	if old, e := s.Store.Get(ctx, "template_batch", input.ID); e == nil {
		batch, e := store.Decode[model.TemplateBatch](old)
		if e != nil {
			return batch, e
		}
		if batch.CreatedBy != p.User.ID || batch.RequestHash != store.Hash(input) {
			return model.TemplateBatch{}, store.ErrConflict
		}
		return s.TemplateBatch(ctx, p, batch.ID)
	} else if !errors.Is(e, store.ErrNotFound) {
		return model.TemplateBatch{}, e
	}
	if s.Mode == "edge" {
		return model.TemplateBatch{}, errors.New("template draft batches are prepared in the cloud")
	}
	return s.prepareTemplateBatch(ctx, p, input, 0, nil, seen, input.RequestID)
}

func (s *Business) RetryTemplateBatch(ctx context.Context, p identity.Principal, id string, input RetryTemplateBatchInput) (model.TemplateBatch, error) {
	batch, err := s.TemplateBatch(ctx, p, id)
	if err != nil {
		return batch, err
	}
	seen, err := s.authorize(ctx, p, "draft", []string{batch.GroupID})
	if err != nil {
		return batch, err
	}
	var replay model.TemplateBatch
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		_, e := tx.BusinessRequest("template-batch:"+id+":"+p.User.ID, input.RequestID, store.Hash([]any{batch.Input, input.ExpectedVersion}), &replay)
		return e
	})
	if err != nil {
		return batch, err
	}
	if replay.ID != "" {
		return s.TemplateBatch(ctx, p, id)
	}
	if batch.Status != "failed" {
		return batch, errors.New("only failed batches can be retried")
	}
	if batch.Version != input.ExpectedVersion {
		return batch, store.ErrConflict
	}
	return s.prepareTemplateBatch(ctx, p, batch.Input, batch.Version, &batch, seen, input.RequestID)
}

func (s *Business) prepareTemplateBatch(ctx context.Context, p identity.Principal, input model.TemplateBatchInput, expected int64, previous *model.TemplateBatch, seen *revisions, requestID string) (model.TemplateBatch, error) {
	now := s.Store.CurrentTime().UnixMilli()
	result := model.TemplateBatch{ID: input.ID, GroupID: input.GroupID, Status: "prepared", Version: expected + 1, CreatedMS: now, UpdatedMS: now, CreatedBy: p.User.ID, Input: input, RequestHash: store.Hash(input), Drafts: []model.Draft{}, Bindings: []model.TemplateBinding{}, Failures: []model.TemplateFailure{}, Attempts: []model.TemplateBatchAttempt{}}
	if previous != nil {
		result.CreatedMS = previous.CreatedMS
		result.CreatedBy = previous.CreatedBy
		result.Attempts = append(result.Attempts, previous.Attempts...)
		if len(result.Attempts) >= 20 {
			return result, errors.New("template retry limit is 20")
		}
	}
	identities := map[string]bool{}
	names := map[string]bool{}
	extraGuards := []definitioncommit.Revision{}
	for _, kind := range []string{"draft", "definition"} {
		docs, err := s.Store.List(ctx, kind)
		if err != nil {
			return result, err
		}
		if len(docs) > 5000 {
			return result, errors.New("template conflict catalogue exceeds 5000 objects")
		}
		members := map[string]int64{}
		for _, doc := range docs {
			members[doc.ID] = doc.Version
			identities[kind+":"+doc.ID] = true
			var d model.Definition
			if kind == "draft" {
				v, e := store.Decode[model.Draft](doc)
				if e != nil {
					return result, e
				}
				d = v.Definition
			} else {
				var e error
				d, e = store.Decode[model.Definition](doc)
				if e != nil {
					return result, e
				}
			}
			names[d.GroupID+"\x00"+d.Name] = true
		}
		extraGuards = append(extraGuards, definitioncommit.Revision{Kind: kind, Members: members})
	}
	instances := map[string]bool{}
	for _, instance := range input.Instances {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		fail := func(code string, err error) {
			result.Failures = append(result.Failures, model.TemplateFailure{InstanceID: instance.ID, Code: code, Message: err.Error()})
		}
		if err := businessID(instance.ID); err != nil {
			fail("identity", err)
			continue
		}
		if instances[instance.ID] {
			fail("conflict", errors.New("instance identity is repeated"))
			continue
		}
		instances[instance.ID] = true
		if strings.TrimSpace(instance.Name) == "" || len(instance.Name) > 150 {
			fail("name", errors.New("instance name must contain 1 to 150 characters"))
			continue
		}
		template, err := scenetemplates.Get(instance.TemplateID, instance.TemplateVersion)
		if err != nil {
			fail("template", err)
			continue
		}
		device, configuration, binding, err := s.templateBinding(ctx, p, input.GroupID, instance, template, seen)
		if err != nil {
			fail("binding", err)
			continue
		}
		if len(template.RequiredActions) > 0 {
			member, e := s.member(ctx, instance.SafetyUserID, []string{device.ID, input.GroupID}, seen)
			if e != nil {
				fail("safety_member", e)
				continue
			}
			if !includesHistory(member.Roles, "safety") && !includesHistory(member.Roles, "admin") {
				fail("safety_member", errors.New("the safety member requires a current safety role"))
				continue
			}
		}
		defs, err := scenetemplates.Instantiate(template, instance.ID, instance.Name, input.GroupID, device.ID, configuration.EdgeID, instance.SafetyUserID, instance.Parameters)
		if err != nil {
			fail("parameters", err)
			continue
		}
		instanceDrafts := []model.Draft{}
		valid := true
		for _, d := range defs {
			for _, kind := range []string{"draft", "definition"} {
				history, e := s.Store.Versions(ctx, kind, d.ID)
				if e != nil {
					return result, e
				}
				if len(history) > 0 {
					identities[kind+":"+d.ID] = true
				}
			}
			if identities["draft:"+d.ID] || identities["definition:"+d.ID] || names[d.GroupID+"\x00"+d.Name] {
				fail("conflict", fmt.Errorf("generated identity or name conflicts: %s", d.ID))
				valid = false
				break
			}
			if err = s.Definitions.access(ctx, p, d, "draft", seen); err != nil {
				fail("permission", err)
				valid = false
				break
			}
			validation := s.Definitions.Engine.Validate(ctx, d)
			if !validation.Valid {
				fail("validation", errors.New(strings.Join(validation.Errors, "; ")))
				valid = false
				break
			}
			instanceDrafts = append(instanceDrafts, model.Draft{ID: d.ID, Definition: d, BaseVersion: 0, Version: 1, AuthorID: p.User.ID, UpdatedMS: now})
		}
		if !valid {
			continue
		}
		for _, d := range instanceDrafts {
			identities["draft:"+d.ID] = true
			names[d.Definition.GroupID+"\x00"+d.Definition.Name] = true
		}
		result.Drafts = append(result.Drafts, instanceDrafts...)
		result.Bindings = append(result.Bindings, binding)
	}
	if len(result.Failures) > 0 {
		result.Status = "failed"
		result.Drafts = []model.Draft{}
	}
	result.Attempts = append(result.Attempts, model.TemplateBatchAttempt{AtMS: now, Actor: p.Actor, Status: result.Status, Failures: append([]model.TemplateFailure{}, result.Failures...)})
	scope := "template-batch:" + input.ID + ":" + p.User.ID
	hash := store.Hash([]any{input, expected})
	var response model.TemplateBatch
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		duplicate, e := tx.BusinessRequest(scope, requestID, hash, &response)
		if e != nil || duplicate {
			return e
		}
		if e = seen.check(tx); e != nil {
			return e
		}
		if e = definitioncommit.CheckRevisions(tx, extraGuards); e != nil {
			return e
		}
		if result.Status == "prepared" {
			for _, draft := range result.Drafts {
				if _, e = tx.Put("draft", draft.ID, 0, draft); e != nil {
					return e
				}
				if e = tx.Audit(p.Actor, "definition.draft.save", draft.Definition.ID, draft.ID, map[string]any{"draft": draft, "template_batch_id": result.ID}); e != nil {
					return e
				}
			}
		}
		if _, e = tx.Put("template_batch", result.ID, expected, result); e != nil {
			return e
		}
		if e = tx.Audit(p.Actor, "template_batch."+result.Status, result.GroupID, result.ID, result); e != nil {
			return e
		}
		if e = tx.CompleteBusinessRequest(scope, requestID, hash, result); e != nil {
			return e
		}
		response = result
		return nil
	})
	return response, err
}

func (s *Business) templateBinding(ctx context.Context, p identity.Principal, group string, instance model.TemplateInstance, template scenetemplates.Template, seen *revisions) (model.Entity, model.ConnectorConfiguration, model.TemplateBinding, error) {
	var device model.Entity
	var configuration model.ConnectorConfiguration
	var binding model.TemplateBinding
	if err := s.access().permit(ctx, p, "read", instance.DeviceID, seen); err != nil {
		return device, configuration, binding, err
	}
	doc, err := s.read(ctx, "entity", instance.DeviceID, seen)
	if err != nil {
		return device, configuration, binding, err
	}
	device, err = store.Decode[model.Entity](doc)
	if err != nil {
		return device, configuration, binding, err
	}
	if device.Kind != "device" || device.Status != "approved" || device.EdgeID == "" {
		return device, configuration, binding, errors.New("binding requires an approved device with an owning edge")
	}
	if instance.DeviceVersion != doc.Version {
		return device, configuration, binding, store.ErrConflict
	}
	if deviceconfig.Protocol(device.Protocol) != template.Protocol {
		return device, configuration, binding, errors.New("device protocol does not match the template")
	}
	found := false
	for id, depth := device.ParentID, 0; id != "" && depth < 64; depth++ {
		if id == group {
			found = true
			break
		}
		d, e := s.read(ctx, "entity", id, seen)
		if e != nil {
			return device, configuration, binding, e
		}
		parent, e := store.Decode[model.Entity](d)
		if e != nil {
			return device, configuration, binding, e
		}
		id = parent.ParentID
	}
	if !found {
		return device, configuration, binding, errors.New("device is outside the target organization asset")
	}
	if err = s.access().permit(ctx, p, "read", device.EdgeID, seen); err != nil {
		return device, configuration, binding, err
	}
	parsed, err := deviceconfig.Validate(deviceconfig.Request{Protocol: device.Protocol, DeviceID: device.ID, Config: device.Config})
	if err != nil {
		return device, configuration, binding, err
	}
	if !parsed.Valid {
		return device, configuration, binding, errors.New("device parameters must be configured and valid before binding")
	}
	keys := map[string]bool{}
	for _, point := range parsed.Parameters.Datapoints {
		key, _ := point["key"].(string)
		keys[key] = true
	}
	for _, key := range template.RequiredKeys {
		if !keys[key] {
			return device, configuration, binding, fmt.Errorf("device lacks required datapoint %s", key)
		}
	}
	id := device.EdgeID + "/" + parsed.Parameters.ConnectorID
	if instance.ConfigurationID != id {
		return device, configuration, binding, errors.New("reviewed connector configuration identity differs from device binding")
	}
	cdoc, err := s.read(ctx, "connector_configuration", id, seen)
	if err != nil {
		return device, configuration, binding, fmt.Errorf("registered connector configuration is required: %w", err)
	}
	configuration, err = store.Decode[model.ConnectorConfiguration](cdoc)
	if err != nil {
		return device, configuration, binding, err
	}
	if configuration.EdgeID != device.EdgeID || configuration.Protocol != template.Protocol {
		return device, configuration, binding, errors.New("connector owner or protocol differs from the device")
	}
	if instance.ConfigurationVersion != configuration.Version {
		return device, configuration, binding, store.ErrConflict
	}
	if err = s.access().permit(ctx, p, "read", configuration.GroupID, seen); err != nil {
		return device, configuration, binding, err
	}
	connector, err := deviceconfig.Validate(deviceconfig.Request{Protocol: configuration.Protocol, Config: configuration.Config})
	if err != nil {
		return device, configuration, binding, err
	}
	if !connector.Valid {
		return device, configuration, binding, errors.New("connector parameters are invalid")
	}
	mappings, _ := connector.Parameters.Converter["action_mappings"].(map[string]any)
	for _, action := range template.RequiredActions {
		if _, ok := mappings[action]; !ok {
			return device, configuration, binding, fmt.Errorf("connector lacks required action mapping %s", action)
		}
	}
	binding = model.TemplateBinding{InstanceID: instance.ID, DeviceID: device.ID, DeviceVersion: doc.Version, ConfigurationID: id, ConfigurationVersion: configuration.Version, ApplicationStatus: "pending", SourceID: configuration.EdgeID}
	if receiptDoc, e := s.Store.Get(ctx, "connector_configuration_receipt", id); e == nil {
		receipt, e := store.Decode[model.ConnectorConfigurationReceipt](receiptDoc)
		if e != nil {
			return device, configuration, binding, e
		}
		if e = seen.remember("connector_configuration_receipt", id, receiptDoc.Version); e != nil {
			return device, configuration, binding, e
		}
		binding.AppliedVersion = receipt.ConfigurationVersion
		binding.ApplicationStatus = receipt.Status
		if receipt.ConfigurationVersion != configuration.Version {
			binding.ApplicationStatus = "pending"
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return device, configuration, binding, e
	}
	return device, configuration, binding, nil
}
