package cloudsync

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Client struct {
	Store           *store.Store
	Identity        *identity.Manager
	URL             string
	HTTP            *http.Client
	CloudSigningKey ed25519.PublicKey
}
type Cursor struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	Audit    int64 `json:"audit"`
}

func (c *Client) Exchange(ctx context.Context) error {
	if !strings.HasPrefix(c.URL, "https://") || c.HTTP == nil || len(c.CloudSigningKey) != ed25519.PublicKeySize {
		return errors.New("sync requires HTTPS, client certificate and pinned cloud signing key")
	}
	var cursor Cursor
	if d, e := c.Store.Get(ctx, "sync_cursor", "cloud"); e == nil {
		cursor, e = store.Decode[Cursor](d)
		if e != nil {
			return e
		}
	} else if !errors.Is(e, store.ErrNotFound) {
		return e
	}
	request := Request{UploadCursor: cursor.Upload, DownloadCursor: cursor.Download}
	changes, e := c.Store.Changes(ctx, cursor.Upload, 500)
	if e != nil {
		return e
	}
	for _, change := range changes {
		request.UploadCursor = change.Sequence
		if change.Document.Kind == "entity" {
			entity, e := store.Decode[model.Entity](change.Document)
			if e != nil {
				return e
			}
			if entity.Kind == "device" && entity.EdgeID == c.Store.NodeID {
				request.Changes = append(request.Changes, change)
			}
		}
		if change.Document.Kind == "asset_proposal" {
			proposal, e := store.Decode[model.AssetProposal](change.Document)
			if e != nil {
				return e
			}
			if proposal.NodeID == c.Store.NodeID && proposal.Status == "pending" {
				request.Changes = append(request.Changes, change)
			}
		}
	}
	for _, kind := range []string{"cloud_observation", "cloud_receipt"} {
		items, e := c.Store.Deliveries(ctx, kind, 1000)
		if e != nil {
			return e
		}
		request.Deliveries = append(request.Deliveries, items...)
	}
	if d, e := c.Store.Get(ctx, "permission_bundle", "active"); e == nil {
		b, e := store.Decode[identity.PermissionBundle](d)
		if e != nil {
			return e
		}
		request.PermissionVersion = b.Version
	}
	request.Audit, e = c.Store.ExportAudit(ctx, cursor.Audit, 500)
	if e != nil {
		return e
	}
	acks, e := c.Store.Deliveries(ctx, "sync_downlink_ack", 1000)
	if e != nil {
		return e
	}
	for _, ack := range acks {
		request.DownlinkAcks = append(request.DownlinkAcks, ack.Destination)
	}
	raw, e := json.Marshal(request)
	if e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/internal/sync/v1/exchange", bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return fmt.Errorf("cloud synchronization HTTP %d: %s", response.StatusCode, string(raw))
	}
	var result Response
	if e = store.DecodeJSONReader(io.LimitReader(response.Body, 16<<20), &result); e != nil {
		return e
	}
	if result.UploadCursor != request.UploadCursor || result.DownloadCursor < cursor.Download || result.AuditSequence < cursor.Audit {
		return errors.New("invalid synchronization acknowledgement cursor")
	}
	importer := &Server{Store: c.Store, Identity: c.Identity}
	if e = importer.importChanges(ctx, c.Store.NodeID, result.Changes, false); e != nil {
		return e
	}
	if result.Permissions != nil {
		if e = c.Identity.ApplyBundle(ctx, *result.Permissions, c.CloudSigningKey); e != nil {
			return e
		}
	}
	offered := map[string]bool{}
	for _, d := range request.Deliveries {
		offered[d.ID] = true
	}
	nextCursor := Cursor{Upload: result.UploadCursor, Download: result.DownloadCursor, Audit: result.AuditSequence}
	changed := nextCursor != cursor || len(request.Changes) > 0 || len(result.Changes) > 0 || len(result.Committed) > 0 || len(acks) > 0 || len(result.Downlinks) > 0
	if changed {
		e = c.Store.Write(ctx, func(t *store.Tx) error {
			for _, change := range request.Changes {
				doc := change.Document
				if doc.Kind == "entity" {
					if _, e := t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", fmt.Sprintf("sync-entity:%s:%d", doc.ID, doc.Version)); e != nil {
						return e
					}
				}
			}
			for _, change := range result.Changes {
				doc := change.Document
				if doc.Kind == "definition" {
					if _, e := t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", fmt.Sprintf("definition-sync:%s:%d", doc.ID, doc.Version)); e != nil {
						return e
					}
				}
			}
			for _, id := range result.Committed {
				if !offered[id] {
					return errors.New("acknowledged unsolicited delivery")
				}
				if _, e := t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", id); e != nil {
					return e
				}
			}
			for _, ack := range acks {
				if _, e := t.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", ack.ID); e != nil {
					return e
				}
			}
			for _, delivery := range result.Downlinks {
				if delivery.Kind != "edge_downlink" || delivery.Destination != c.Store.NodeID {
					return errors.New("downlink destination mismatch")
				}
				var execution model.Execution
				if e := store.DecodeJSON(delivery.Payload, &execution); e != nil {
					return e
				}
				if execution.DownlinkID == "" || delivery.ID != "downlink:"+execution.DownlinkID {
					return errors.New("invalid execution identifier")
				}
				duplicate, e := t.Inbox("cloud-downlink:"+delivery.ID, store.Hash(delivery.Payload), "cloud")
				if e != nil {
					return e
				}
				if !duplicate {
					if e = t.Enqueue(delivery.ID, "edge_downlink", c.Store.NodeID, execution); e != nil {
						return e
					}
				}
				if e = t.Enqueue("ack:"+delivery.ID, "sync_downlink_ack", delivery.ID, map[string]bool{"committed": true}); e != nil {
					return e
				}
			}
			if nextCursor == cursor {
				return nil
			}
			return t.SetEphemeral("sync_cursor", "cloud", nextCursor)
		})
	}
	if e != nil {
		return e
	}
	if len(result.Failures) > 0 {
		return fmt.Errorf("cloud deferred %d deliveries: %v", len(result.Failures), result.Failures)
	}
	return nil
}
