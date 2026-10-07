package cloudsync

import (
	"context"
	"errors"
	"strings"

	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func businessKind(kind string) bool {
	return kind == "alarm_operation" || kind == "connector_configuration" || kind == "connector_configuration_receipt"
}
func hasBusiness(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

// Imported operations use current local users and grants inside the write
// transaction, including every ancestor consulted by resource authorization.
func businessPermit(tx *store.Tx, userID, action, resource string) (model.User, error) {
	var user model.User
	if resource == "" {
		return user, identity.ErrDenied
	}
	d, err := tx.Get("user", userID)
	if err != nil {
		return user, identity.ErrDenied
	}
	user, err = store.Decode[model.User](d)
	if err != nil {
		return user, err
	}
	if !user.Active || user.AI {
		return user, identity.ErrDenied
	}
	allowed := false
	for _, role := range user.Roles {
		if role == "admin" || role == "engineer" || ((role == "leader" || role == "safety") && (action == "read" || action == "approve")) || role == "viewer" && action == "read" {
			allowed = true
		}
	}
	resources := append([]string{}, user.Resources...)
	grants, err := tx.List("grant")
	if err != nil {
		return user, err
	}
	for _, d := range grants {
		g, e := store.Decode[identity.Grant](d)
		if e != nil {
			return user, e
		}
		if hasBusiness(user.Teams, g.TeamID) && hasBusiness(g.Actions, action) {
			allowed = true
			resources = append(resources, g.Resources...)
			resources = append(resources, g.GroupID)
		}
	}
	if !allowed {
		return user, identity.ErrDenied
	}
	if hasBusiness(resources, "*") || hasBusiness(resources, resource) {
		return user, nil
	}
	seen := map[string]bool{}
	for id, depth := resource, 0; id != "" && depth < 64; depth++ {
		if seen[id] {
			return user, identity.ErrDenied
		}
		seen[id] = true
		d, e := tx.Get("entity", id)
		if e != nil {
			return user, identity.ErrDenied
		}
		entity, e := store.Decode[model.Entity](d)
		if e != nil {
			return user, e
		}
		id = entity.ParentID
		if hasBusiness(resources, id) {
			return user, nil
		}
	}
	return user, identity.ErrDenied
}

func (s *Server) importBusiness(tx *store.Tx, node string, d store.Document, fromEdge bool) error {
	if d.Kind == "alarm_operation" {
		op, err := store.Decode[model.AlarmOperation](d)
		if err != nil {
			return err
		}
		if op.ID != d.ID || op.Version != d.Version || fromEdge && op.SourceID != node {
			return errors.New("alarm operation source or identity mismatch")
		}
		doc, err := tx.Get("entity", op.EntityID)
		if err != nil {
			return err
		}
		entity, err := store.Decode[model.Entity](doc)
		if err != nil {
			return err
		}
		if entity.Kind != "device" || entity.EdgeID != node {
			return errors.New("alarm operation device owner mismatch")
		}
		if prior, e := tx.Get("alarm_operation", op.ID); e == nil {
			old, e := store.Decode[model.AlarmOperation](prior)
			if e != nil {
				return e
			}
			if store.Hash(old) != store.Hash(op) {
				return store.ErrConflict
			}
			return nil
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if op.Actor.AI {
			return identity.ErrDenied
		}
		if _, err = businessPermit(tx, op.Actor.UserID, "approve", entity.ID); err != nil {
			return err
		}
		defDoc, err := tx.Get("definition", op.DefinitionID)
		if err != nil {
			return err
		}
		def, err := store.Decode[model.Definition](defDoc)
		if err != nil {
			return err
		}
		if _, err = businessPermit(tx, op.Actor.UserID, "read", def.GroupID); err != nil {
			return err
		}
		if aDoc, e := tx.Get("alarm", op.AlarmID); e == nil {
			a, e := store.Decode[model.Alarm](aDoc)
			if e != nil {
				return e
			}
			if a.EntityID != op.EntityID || a.DefinitionID != op.DefinitionID {
				return store.ErrConflict
			}
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if op.Action == "assign" {
			member, e := businessPermit(tx, op.AssigneeID, "approve", entity.ID)
			if e != nil {
				return e
			}
			if member.DepartmentID == "" {
				return identity.ErrDenied
			}
			if _, e = tx.Get("department", member.DepartmentID); e != nil {
				return e
			}
		}
		if _, err = tx.ApplyAlarmOperation(op); err != nil {
			return err
		}
		return tx.Audit(op.Actor, "alarm.synchronized", op.EntityID, op.ID, map[string]any{"source_id": op.SourceID, "source_sequence": op.SourceSequence, "action": op.Action})
	}
	if d.Kind == "connector_configuration" {
		c, err := store.Decode[model.ConnectorConfiguration](d)
		if err != nil {
			return err
		}
		if c.ID != d.ID || c.Version != d.Version || c.EdgeID != node || c.ID != c.EdgeID+"/"+c.ConnectorID {
			return errors.New("connector configuration owner or identity mismatch")
		}
		if _, err = businessPermit(tx, c.Actor.UserID, "register", c.GroupID); err != nil {
			return err
		}
		if _, err = businessPermit(tx, c.Actor.UserID, "register", c.EdgeID); err != nil {
			return err
		}
		parsed, err := deviceconfig.Validate(deviceconfig.Request{Protocol: c.Protocol, Config: c.Config})
		if err != nil {
			return err
		}
		if !parsed.Valid || parsed.Parameters.Kind != "connector" || parsed.Parameters.ConnectorID != c.ConnectorID {
			return errors.New("invalid connector configuration")
		}
		if _, ok := parsed.Parameters.Connection["password"]; ok {
			return errors.New("connector credential must remain on the owning edge")
		}
		if c.CredentialRef != "" && c.CredentialRef != model.ConnectorSecretReference(c.ID, c.Version) {
			return errors.New("invalid connector credential reference")
		}
	} else if d.Kind == "connector_configuration_receipt" {
		r, err := store.Decode[model.ConnectorConfigurationReceipt](d)
		if err != nil {
			return err
		}
		if r.SourceID != node || r.ID != d.ID || r.Version != d.Version || r.ConfigurationID != r.ID || !strings.HasPrefix(r.ID, node+"/") || (r.Status != "applied" && r.Status != "failed") {
			return errors.New("invalid connector receipt owner or identity")
		}
		cDoc, err := tx.Get("connector_configuration", r.ID)
		if err != nil {
			return err
		}
		c, err := store.Decode[model.ConnectorConfiguration](cDoc)
		if err != nil {
			return err
		}
		if c.EdgeID != node || r.ConfigurationVersion < 1 || r.ConfigurationVersion > c.Version {
			return store.ErrConflict
		}
	} else {
		return errors.New("unsupported business document")
	}
	changed, err := tx.ImportDocument(d)
	if err != nil {
		return err
	}
	if d.Kind == "connector_configuration" {
		c, e := store.Decode[model.ConnectorConfiguration](d)
		if e != nil {
			return e
		}
		id := model.ConnectorSecretReference(c.ID, c.Version)
		if old, e := tx.Read("connector_configuration_version", id); e == nil {
			previous, e := store.Decode[model.ConnectorConfiguration](old)
			if e != nil {
				return e
			}
			if store.Hash(previous) != store.Hash(c) {
				return store.ErrConflict
			}
		} else if errors.Is(e, store.ErrNotFound) {
			if _, e = tx.Put("connector_configuration_version", id, 0, c); e != nil {
				return e
			}
		} else {
			return e
		}
	}
	if changed && fromEdge {
		return tx.RecordChange(d)
	}
	return nil
}

func (s *Server) businessDestination(ctx context.Context, node string, d store.Document) (bool, error) {
	switch d.Kind {
	case "alarm_operation":
		op, e := store.Decode[model.AlarmOperation](d)
		if e != nil {
			return false, e
		}
		doc, e := s.Store.Get(ctx, "entity", op.EntityID)
		if e != nil {
			return false, e
		}
		entity, e := store.Decode[model.Entity](doc)
		return entity.EdgeID == node, e
	case "connector_configuration":
		c, e := store.Decode[model.ConnectorConfiguration](d)
		return c.EdgeID == node, e
	case "connector_configuration_receipt":
		c, e := store.Decode[model.ConnectorConfigurationReceipt](d)
		return c.SourceID == node, e
	}
	return true, nil
}

func businessUpload(node string, d store.Document) (bool, error) {
	switch d.Kind {
	case "alarm_operation":
		op, e := store.Decode[model.AlarmOperation](d)
		return op.SourceID == node, e
	case "connector_configuration":
		c, e := store.Decode[model.ConnectorConfiguration](d)
		return c.EdgeID == node, e
	case "connector_configuration_receipt":
		c, e := store.Decode[model.ConnectorConfigurationReceipt](d)
		return c.SourceID == node, e
	}
	return false, nil
}
