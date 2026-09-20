package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func proposalResource(p model.AssetProposal) string {
	if p.BaseVersion > 0 {
		return p.Entity.ID
	}
	if p.Entity.ParentID != "" {
		return p.Entity.ParentID
	}
	return "*"
}
func (s *Server) proposeAsset(w http.ResponseWriter, r *http.Request, p identity.Principal, entity model.Entity, base int64) error {
	now := s.Store.Now().UnixMilli()
	proposal := model.AssetProposal{ID: s.NodeID + ":" + identity.ID(), NodeID: s.NodeID, Entity: entity, BaseVersion: base, Status: "pending", Actor: p.Actor, CreatedMS: now, UpdatedMS: now, Version: 1}
	err := s.Store.Write(r.Context(), func(tx *store.Tx) error {
		if _, e := tx.Put("asset_proposal", proposal.ID, 0, proposal); e != nil {
			return e
		}
		return tx.Audit(p.Actor, "asset.propose", proposalResource(proposal), proposal.ID, proposal)
	})
	if err == nil {
		respond(w, 202, proposal)
	}
	return err
}
func (s *Server) assetProposals(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	docs, err := s.Store.List(r.Context(), "asset_proposal")
	if err != nil {
		return err
	}
	result := []model.AssetProposal{}
	for _, doc := range docs {
		proposal, e := store.Decode[model.AssetProposal](doc)
		if e != nil {
			return e
		}
		if s.allow(r, p, "read", proposalResource(proposal)) == nil {
			result = append(result, proposal)
		}
	}
	respond(w, 200, result)
	return nil
}
func (s *Server) decideAssetProposal(w http.ResponseWriter, r *http.Request, p identity.Principal) error {
	if s.Mode != "cloud" {
		return errors.New("asset proposals are reviewed in the cloud")
	}
	var req struct {
		Approve         bool   `json:"approve"`
		Reason          string `json:"reason"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if err := decode(r, &req); err != nil {
		return err
	}
	if strings.TrimSpace(req.Reason) == "" {
		return errors.New("decision reason required")
	}
	doc, err := s.Store.Get(r.Context(), "asset_proposal", r.PathValue("id"))
	if err != nil {
		return err
	}
	proposal, err := store.Decode[model.AssetProposal](doc)
	if err != nil {
		return err
	}
	if err = s.allow(r, p, "register", proposalResource(proposal)); err != nil {
		return err
	}
	if proposal.Status != "pending" || req.ExpectedVersion != doc.Version {
		return store.ErrConflict
	}
	if proposal.Entity.Kind != "asset" {
		return errors.New("proposal must contain a logical asset")
	}
	if proposal.Entity.ParentID != "" {
		if err = s.allow(r, p, "register", proposal.Entity.ParentID); err != nil {
			return err
		}
	}
	proposal.Status = "rejected"
	if req.Approve {
		proposal.Status = "approved"
	}
	proposal.Reason = req.Reason
	proposal.DecisionActor = p.Actor
	proposal.UpdatedMS = s.Store.Now().UnixMilli()
	proposal.Version = doc.Version + 1
	err = s.Store.Write(r.Context(), func(tx *store.Tx) error {
		if req.Approve {
			entity := proposal.Entity
			entity.Version = proposal.BaseVersion + 1
			if entity.Status == "proposed" || entity.Status == "" {
				entity.Status = "active"
			}
			for id, depth := entity.ParentID, 0; id != ""; depth++ {
				if id == entity.ID || depth >= 64 {
					return errors.New("asset hierarchy contains a cycle")
				}
				parentDoc, e := tx.Get("entity", id)
				if e != nil {
					return e
				}
				parent, e := store.Decode[model.Entity](parentDoc)
				if e != nil {
					return e
				}
				if parent.Kind != "asset" {
					return errors.New("parent must be a logical asset")
				}
				id = parent.ParentID
			}
			if _, e := tx.Put("entity", entity.ID, proposal.BaseVersion, entity); e != nil {
				return e
			}
			if e := tx.Enqueue(fmt.Sprintf("entity:%s:%d", entity.ID, entity.Version), "tb_entity", entity.ID, entity); e != nil {
				return e
			}
		}
		if _, e := tx.Put("asset_proposal", proposal.ID, doc.Version, proposal); e != nil {
			return e
		}
		return tx.Audit(p.Actor, "asset."+proposal.Status, proposalResource(proposal), proposal.ID, proposal)
	})
	if err == nil {
		respond(w, 200, proposal)
	}
	return err
}
