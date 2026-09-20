// Package cloudsync exchanges durable business records over mutually authenticated
// TLS. Every acknowledgement names a record that is already committed locally.
package cloudsync

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Server struct {
	Store    *store.Store
	Identity *identity.Manager
}
type Request struct {
	Changes           []store.Change        `json:"changes"`
	UploadCursor      int64                 `json:"upload_cursor"`
	DownloadCursor    int64                 `json:"download_cursor"`
	PermissionVersion int64                 `json:"permission_version"`
	Deliveries        []store.Delivery      `json:"deliveries"`
	Audit             []store.AuditTransfer `json:"audit"`
	DownlinkAcks      []string              `json:"downlink_acks"`
}
type Response struct {
	Committed      []string                   `json:"committed"`
	Failures       map[string]string          `json:"failures"`
	UploadCursor   int64                      `json:"upload_cursor"`
	Changes        []store.Change             `json:"changes"`
	DownloadCursor int64                      `json:"download_cursor"`
	AuditSequence  int64                      `json:"audit_sequence"`
	Permissions    *identity.PermissionBundle `json:"permissions,omitempty"`
	Downlinks      []store.Delivery           `json:"downlinks"`
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /internal/sync/v1/exchange", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
			http.Error(w, "mutual TLS required", http.StatusUnauthorized)
			return
		}
		node, registration, e := s.Admit(r.Context(), r.TLS.PeerCertificates[0])
		if e != nil {
			http.Error(w, "node admission failed", http.StatusForbidden)
			return
		}
		var request Request
		if e = store.DecodeJSONReader(http.MaxBytesReader(w, r.Body, 16<<20), &request); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		response, e := s.Exchange(r.Context(), node, registration, request)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
	})
	return mux
}
func (s *Server) Exchange(ctx context.Context, node string, registration Registration, r Request) (Response, error) {
	result := Response{Failures: map[string]string{}, Committed: []string{}, Changes: []store.Change{}, Downlinks: []store.Delivery{}}
	if len(r.Deliveries) > 2000 || len(r.Changes) > 1000 || len(r.Audit) > 1000 || len(r.DownlinkAcks) > 1000 {
		return result, errors.New("sync batch exceeds limits")
	}
	if e := s.acknowledgeCursor(ctx, node, r.DownloadCursor); e != nil {
		return result, e
	}
	if e := s.importChanges(ctx, node, r.Changes, true); e != nil {
		return result, e
	}
	result.UploadCursor = r.UploadCursor
	observations := []store.IngestBatch{}
	observationIDs := []string{}
	flush := func() {
		if len(observations) == 0 {
			return
		}
		if _, err := s.Store.IngestMessages(ctx, observations); err == nil {
			result.Committed = append(result.Committed, observationIDs...)
		} else {
			// Isolate malformed or conflicting messages without acknowledging them.
			for i, batch := range observations {
				if _, err := s.Store.Ingest(ctx, batch); err != nil {
					result.Failures[observationIDs[i]] = err.Error()
				} else {
					result.Committed = append(result.Committed, observationIDs[i])
				}
			}
		}
		observations = observations[:0]
		observationIDs = observationIDs[:0]
	}
	pointCount := 0
	for _, delivery := range r.Deliveries {
		if delivery.Kind == "cloud_observation" {
			batch, err := s.observation(ctx, node, delivery)
			if err != nil {
				result.Failures[delivery.ID] = err.Error()
				continue
			}
			if len(observations) >= 256 || pointCount+len(batch.Points) > 10000 {
				flush()
				pointCount = 0
			}
			observations = append(observations, batch)
			observationIDs = append(observationIDs, delivery.ID)
			pointCount += len(batch.Points)
		} else if err := s.receive(ctx, node, delivery); err != nil {
			result.Failures[delivery.ID] = err.Error()
		} else {
			result.Committed = append(result.Committed, delivery.ID)
		}
	}
	flush()
	if len(r.DownlinkAcks) > 0 {
		if err := s.Store.Write(ctx, func(t *store.Tx) error {
			for _, id := range r.DownlinkAcks {
				if _, err := t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1 AND kind='edge_downlink' AND destination=$2", id, node); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return result, err
		}
	}
	public, e := base64.StdEncoding.DecodeString(registration.AuditPublicKey)
	if e != nil {
		return result, e
	}
	result.AuditSequence, e = s.Store.ImportAudit(ctx, node, ed25519.PublicKey(public), r.Audit)
	if e != nil {
		return result, e
	}
	changes, e := s.Store.Changes(ctx, r.DownloadCursor, 500)
	if e != nil {
		return result, e
	}
	result.DownloadCursor = r.DownloadCursor
	for _, change := range changes {
		result.DownloadCursor = change.Sequence
		if change.Document.Kind == "entity" {
			entity, e := store.Decode[model.Entity](change.Document)
			if e != nil {
				return result, e
			}
			if entity.Kind == "device" && entity.EdgeID == node {
				continue
			}
		}
		if change.Document.Kind == "asset_proposal" {
			proposal, e := store.Decode[model.AssetProposal](change.Document)
			if e != nil {
				return result, e
			}
			if proposal.NodeID != node {
				continue
			}
		}
		result.Changes = append(result.Changes, change)
	}
	// Cached signed packages avoid creating a new identity history on every poll.
	bundleID := "permissions:" + node
	var bundle identity.PermissionBundle
	d, e := s.Store.Get(ctx, "sync_permission", bundleID)
	if e == nil {
		bundle, e = store.Decode[identity.PermissionBundle](d)
	}
	if e != nil && !errors.Is(e, store.ErrNotFound) {
		return result, e
	}
	// A content digest refreshes immediately on personnel, role or credential changes.
	digest, e := s.permissionDigest(ctx)
	if e != nil {
		return result, e
	}
	digest = store.Hash([]string{digest, registration.EncryptionPublicKey})
	digestDoc, _ := s.Store.Get(ctx, "sync_permission_digest", node)
	oldDigest, _ := store.Decode[string](digestDoc)
	if bundle.Version == 0 || bundle.ExpiresMS <= s.Store.Now().Add(24*time.Hour).UnixMilli() || oldDigest != digest {
		version := s.Store.Now().UnixMilli()
		if version <= bundle.Version {
			version = bundle.Version + 1
		}
		bundle, e = s.Identity.SignBundleForNode(ctx, node, version, registration.EncryptionPublicKey)
		if e != nil {
			return result, e
		}
		if e = s.Store.Write(ctx, func(t *store.Tx) error {
			if e := t.SetEphemeral("sync_permission", bundleID, bundle); e != nil {
				return e
			}
			return t.SetEphemeral("sync_permission_digest", node, digest)
		}); e != nil {
			return result, e
		}
	}
	if r.PermissionVersion < bundle.Version {
		result.Permissions = &bundle
	}
	items, e := s.Store.Deliveries(ctx, "edge_downlink", 1000)
	if e != nil {
		return result, e
	}
	for _, item := range items {
		if item.Destination == node {
			result.Downlinks = append(result.Downlinks, item)
		}
	}
	if e = s.heartbeat(ctx, node); e != nil {
		return result, e
	}
	if e := s.offeredCursor(ctx, node, result.DownloadCursor); e != nil {
		return result, e
	}
	return result, nil
}

func (s *Server) heartbeat(ctx context.Context, node string) error {
	now := s.Store.Now().UnixMilli()
	if doc, err := s.Store.Get(ctx, "source", node); err == nil {
		state, err := store.Decode[model.SourceState](doc)
		if err != nil {
			return err
		}
		if state.Status == "online" && now >= state.LastSeenMS && now-state.LastSeenMS < s.Store.Policy().HeartbeatMS {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return s.Store.Write(ctx, func(t *store.Tx) error {
		state := model.SourceState{ID: node, Status: "online", Backfill: "complete"}
		if doc, err := t.Get("source", node); err == nil {
			state, err = store.Decode[model.SourceState](doc)
			if err != nil {
				return err
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if state.Status == "online" && now >= state.LastSeenMS && now-state.LastSeenMS < s.Store.Policy().HeartbeatMS {
			return nil
		}
		state.LastSeenMS = now
		state.Status = "online"
		state.Reason = ""
		return t.SetEphemeral("source", node, state)
	})
}
func (s *Server) permissionDigest(ctx context.Context) (string, error) {
	all := []store.Document{}
	for _, kind := range []string{"user", "grant", "credential"} {
		docs, e := s.Store.List(ctx, kind)
		if e != nil {
			return "", e
		}
		all = append(all, docs...)
	}
	return store.Hash(all), nil
}
func (s *Server) receive(ctx context.Context, node string, d store.Delivery) error {
	switch d.Kind {
	case "cloud_observation":
		batch, err := s.observation(ctx, node, d)
		if err != nil {
			return err
		}
		_, err = s.Store.Ingest(ctx, batch)
		return err
	case "cloud_receipt":
		var receipt model.Execution
		if e := store.DecodeJSON(d.Payload, &receipt); e != nil {
			return e
		}
		defDoc, e := s.Store.Get(ctx, "definition", receipt.DefinitionID)
		if e != nil {
			return e
		}
		def, e := store.Decode[model.Definition](defDoc)
		if e != nil {
			return e
		}
		owned := false
		for _, edge := range def.Policy.EdgeIDs {
			owned = owned || edge == node
		}
		if !owned {
			return errors.New("execution does not belong to this node")
		}
		return s.Store.Write(ctx, func(t *store.Tx) error {
			duplicate, e := t.Inbox("sync-receipt:"+d.ID, store.Hash(d.Payload), node)
			if e != nil || duplicate {
				return e
			}
			old, e := t.Get("execution", receipt.DownlinkID)
			if e != nil && !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if e == nil {
				previous, e := store.Decode[model.Execution](old)
				if e != nil {
					return e
				}
				if previous.Binding != receipt.Binding || previous.DefinitionVersion != receipt.DefinitionVersion {
					return store.ErrConflict
				}
			}
			cursorID := node + ":" + receipt.DownlinkID
			if current, e := t.Get("receipt_cursor", cursorID); e == nil {
				v, e := store.Decode[int64](current)
				if e != nil {
					return e
				}
				if v >= receipt.Version {
					return nil
				}
			}
			sourceVersion := receipt.Version
			receipt.Version = old.Version + 1
			if _, e = t.Put("execution", receipt.DownlinkID, old.Version, receipt); e != nil {
				return e
			}
			return t.SetEphemeral("receipt_cursor", cursorID, sourceVersion)
		})
	default:
		return fmt.Errorf("unsupported uplink kind %s", d.Kind)
	}
}
func (s *Server) importChanges(ctx context.Context, node string, changes []store.Change, fromEdge bool) error {
	if len(changes) == 0 {
		return nil
	}
	return s.Store.Write(ctx, func(t *store.Tx) error {
		for _, change := range changes {
			d := change.Document
			if fromEdge {
				if d.Kind != "entity" && d.Kind != "asset_proposal" {
					return errors.New("edge cannot publish cloud definitions")
				}
				if d.Kind == "entity" {
					entity, e := store.Decode[model.Entity](d)
					if e != nil {
						return e
					}
					if entity.Kind != "device" || entity.EdgeID != node {
						return errors.New("physical device owner mismatch")
					}
				}
				if d.Kind == "asset_proposal" {
					proposal, e := store.Decode[model.AssetProposal](d)
					if e != nil {
						return e
					}
					if proposal.NodeID != node || proposal.Entity.Kind != "asset" || proposal.Status != "pending" || proposal.Version != 1 || !strings.HasPrefix(proposal.ID, node+":") {
						return errors.New("invalid edge asset proposal")
					}
				}
			} else {
				switch d.Kind {
				case "definition", "department", "dashboard":
				case "asset_proposal":
					proposal, e := store.Decode[model.AssetProposal](d)
					if e != nil {
						return e
					}
					if proposal.NodeID != node {
						return errors.New("asset proposal destination mismatch")
					}
				case "entity":
					entity, e := store.Decode[model.Entity](d)
					if e != nil {
						return e
					}
					if entity.Kind == "device" && entity.EdgeID == node {
						return errors.New("cloud cannot approve this node's physical devices")
					}
				default:
					return errors.New("unsupported cloud document")
				}
			}
			applied, e := t.ImportDocument(d)
			if e != nil {
				return e
			}
			if !applied {
				continue
			}
			if fromEdge {
				if e = t.RecordChange(d); e != nil {
					return e
				}
			}
			kind := "tb_entity"
			if d.Kind == "definition" {
				kind = "tb_definition"
			} else if d.Kind != "entity" {
				continue
			}
			if e = t.Enqueue(fmt.Sprintf("sync-native:%s:%s:%d", d.Kind, d.ID, d.Version), kind, d.ID, d.Data); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Server) observation(ctx context.Context, node string, d store.Delivery) (store.IngestBatch, error) {
	var batch store.IngestBatch
	if e := store.DecodeJSON(d.Payload, &batch); e != nil {
		return store.IngestBatch{}, e
	}
	devices := map[string]bool{}
	for _, p := range batch.Points {
		devices[p.DeviceID] = true
	}
	for _, gap := range batch.Gaps {
		devices[gap.DeviceID] = true
	}
	for device := range devices {
		entityDoc, e := s.Store.Get(ctx, "entity", device)
		if e != nil {
			return store.IngestBatch{}, e
		}
		entity, e := store.Decode[model.Entity](entityDoc)
		if e != nil {
			return store.IngestBatch{}, e
		}
		if entity.Kind != "device" || entity.EdgeID != node || entity.Status != "approved" {
			return store.IngestBatch{}, errors.New("observation device is not approved for this edge")
		}
	}
	if batch.SourceID != node {
		return store.IngestBatch{}, errors.New("observation source differs from certificate identity")
	}
	if d.ID != "upstream:"+batch.MessageID {
		return store.IngestBatch{}, errors.New("delivery identifier mismatch")
	}
	// The original protocol hash is retained; the synchronization envelope has its
	// own content hash so changed JSON cannot reuse an accepted protocol digest.
	envelope := batch
	envelope.PayloadHash = ""
	batch.PayloadHash = store.Hash(envelope)
	return batch, nil
}
