package application

import (
	"context"
	"encoding/json"
	"errors"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// definitionEvidence captures the identities and versions authorized for this
// particular definition body. Subsequent edits cannot erase its resource set.
func (s *Investigations) definitionEvidence(ctx context.Context, p identity.Principal, d model.Definition, seen *revisions, published bool) ([]model.EvidenceResource, error) {
	resources := []model.EvidenceResource{}
	visited := map[string]bool{}
	var visit func(model.Definition, bool, int) error
	visit = func(current model.Definition, include bool, depth int) error {
		if depth > 64 {
			return errors.New("definition dependency depth exceeded")
		}
		if visited[current.ID] {
			return nil
		}
		visited[current.ID] = true
		if err := s.Definitions.access(ctx, p, current, "read", seen); err != nil {
			return err
		}
		if include {
			resources = append(resources, model.EvidenceResource{Kind: "definition", ID: current.ID, Version: current.Version})
		}
		resources = append(resources, model.EvidenceResource{Kind: "resource", ID: current.GroupID, Version: seen.documents[documentKey{"entity", current.GroupID}]})
		for _, id := range definitionResources(current) {
			if id == "" || id == current.GroupID {
				continue
			}
			resources = append(resources, model.EvidenceResource{Kind: "entity", ID: id, Version: seen.documents[documentKey{"entity", id}]})
		}
		for _, id := range current.Dependencies {
			other, err := s.Definitions.definition(ctx, id, seen)
			if err != nil {
				return err
			}
			if err = visit(other, true, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	err := visit(d, published, 0)
	return resources, err
}

func (s *Investigations) ReadDefinitionEvidence(ctx context.Context, p identity.Principal, id string) (map[string]any, error) {
	seen, err := s.authorize(ctx, p)
	if err != nil {
		return nil, err
	}
	d, err := s.Definitions.definition(ctx, id, seen)
	if err != nil {
		return nil, err
	}
	resources, err := s.definitionEvidence(ctx, p, d, seen, true)
	if err != nil {
		return nil, err
	}
	if err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) }); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	var value map[string]any
	if err = store.DecodeJSON(raw, &value); err != nil {
		return nil, err
	}
	value["_resources"] = resources
	return value, nil
}

func (s *Investigations) ReadDraftEvidence(ctx context.Context, p identity.Principal, id string) (map[string]any, error) {
	doc, err := s.Store.Get(ctx, "draft", id)
	if err != nil {
		return nil, err
	}
	return s.readAIDocument(ctx, p, doc)
}

func (s *Investigations) DecorateDraftResult(ctx context.Context, p identity.Principal, id string, version int64, value any, additional []model.EvidenceResource) (map[string]any, error) {
	doc, err := s.Store.Get(ctx, "draft", id)
	if err != nil {
		return nil, err
	}
	if doc.Version != version {
		return nil, store.ErrConflict
	}
	canonical, err := s.readAIDocument(ctx, p, doc)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err = store.DecodeJSON(raw, &result); err != nil {
		return nil, err
	}
	resources, _ := canonical["_resources"].([]model.EvidenceResource)
	result["_resources"] = append(resources, additional...)
	result["draft_version"] = version
	return result, nil
}

func (s *Investigations) readAIDocument(ctx context.Context, p identity.Principal, doc store.Document) (map[string]any, error) {
	seen, err := s.authorize(ctx, p)
	if err != nil {
		return nil, err
	}
	if err = seen.remember(doc.Kind, doc.ID, doc.Version); err != nil {
		return nil, err
	}
	var value map[string]any
	if err = store.DecodeJSON(doc.Data, &value); err != nil {
		return nil, err
	}
	resources := []model.EvidenceResource{{Kind: doc.Kind, ID: doc.ID, Version: doc.Version}}
	switch doc.Kind {
	case "definition":
		d, e := store.Decode[model.Definition](doc)
		if e != nil {
			return nil, e
		}
		parts, e := s.definitionEvidence(ctx, p, d, seen, true)
		if e != nil {
			return nil, e
		}
		resources = append(resources, parts...)
	case "draft":
		d, e := store.Decode[model.Draft](doc)
		if e != nil {
			return nil, e
		}
		parts, e := s.definitionEvidence(ctx, p, d.Definition, seen, false)
		if e != nil {
			return nil, e
		}
		resources = append(resources, parts...)
	case "catalogue":
		id, _ := value["definition_id"].(string)
		if id != "" {
			d, e := s.Definitions.definition(ctx, id, seen)
			if e != nil {
				return nil, e
			}
			parts, e := s.definitionEvidence(ctx, p, d, seen, true)
			if e != nil {
				return nil, e
			}
			resources = append(resources, parts...)
		} else {
			group, _ := value["group_id"].(string)
			if group == "" {
				group = "*"
			}
			if err = s.Definitions.permit(ctx, p, "read", group, seen); err != nil {
				return nil, err
			}
			resources = append(resources, model.EvidenceResource{Kind: "resource", ID: group})
		}
	default:
		return nil, errors.New("invalid AI document collection")
	}
	if err = s.Store.Write(ctx, func(tx *store.Tx) error { return seen.check(tx) }); err != nil {
		return nil, err
	}
	value["version"] = doc.Version
	value["_resources"] = resources
	return value, nil
}
