package cloudsync

import (
	"context"
	"errors"
	"fmt"

	"competition2026/product/platform/internal/control"
	"competition2026/product/platform/internal/engine"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) receiveExecutionReceipt(ctx context.Context, node string, delivery store.Delivery) error {
	var envelope model.ExecutionReceipt
	if err := store.DecodeJSON(delivery.Payload, &envelope); err != nil {
		return err
	}
	receipt := envelope.Execution
	if receipt.Version < 1 || delivery.ID != fmt.Sprintf("receipt:%s:%d", receipt.DownlinkID, receipt.Version) {
		return errors.New("invalid execution receipt identity")
	}
	defDoc, err := s.Store.Get(ctx, "definition", receipt.DefinitionID)
	if err != nil {
		return err
	}
	definition, err := store.Decode[model.Definition](defDoc)
	if err != nil {
		return err
	}
	owned := false
	for _, edge := range definition.Policy.EdgeIDs {
		owned = owned || edge == node
	}
	if !owned {
		return errors.New("execution does not belong to this node")
	}
	if source := envelope.ReceiptSource; source != nil {
		if source.SchemaVersion != 1 || source.NodeID != node || source.Version != receipt.Version || source.RecordedMS <= 0 {
			return errors.New("execution receipt source does not match authenticated delivery")
		}
	} else if len(envelope.CommandEvidence) != 0 {
		return errors.New("execution evidence requires authenticated receipt source metadata")
	}
	if len(envelope.CommandEvidence) != 0 {
		original, err := (&engine.Service{Store: s.Store}).Version(ctx, receipt.DefinitionID, receipt.DefinitionVersion)
		if err != nil {
			return err
		}
		for _, item := range envelope.CommandEvidence {
			if err := control.ValidateReceiptEvidence(receipt, original, item); err != nil {
				return err
			}
			if item.CollectedMS > envelope.ReceiptSource.RecordedMS+5000 {
				return errors.New("execution evidence was collected after the source receipt")
			}
		}
	}
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		if err := tx.CheckRevisions([]store.Revision{{Kind: "definition", ID: defDoc.ID, Version: defDoc.Version}}); err != nil {
			return err
		}
		duplicate, err := tx.Inbox("sync-receipt:"+delivery.ID, store.Hash(delivery.Payload), node)
		if err != nil || duplicate {
			return err
		}
		old, err := tx.Get("execution", receipt.DownlinkID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		var previous model.Execution
		if err == nil {
			previous, err = store.Decode[model.Execution](old)
			if err != nil {
				return err
			}
			if previous.Binding != receipt.Binding || previous.DefinitionID != receipt.DefinitionID || previous.DefinitionVersion != receipt.DefinitionVersion || previous.Override != receipt.Override || store.Hash(previous.Params) != store.Hash(receipt.Params) {
				return store.ErrConflict
			}
		}
		ids := []string{}
		for _, item := range envelope.CommandEvidence {
			if err := tx.SaveControlEvidence(item); err != nil {
				return err
			}
			ids = append(ids, item.ID)
		}
		if envelope.ReceiptSource != nil {
			all, err := tx.ExecutionEvidence(receipt.DownlinkID)
			if err != nil {
				return err
			}
			byID := map[string]model.CommandEvidence{}
			for _, item := range all {
				byID[item.ID] = item
			}
			for _, result := range receipt.Steps {
				if result.EvidenceID == "" {
					continue
				}
				item, ok := byID[result.EvidenceID]
				if !ok || !item.Trusted || item.CommandID != result.CommandID || item.StepID != result.StepID || item.Status != result.Status {
					return errors.New("execution result references missing or conflicting command evidence")
				}
			}
		}
		if len(ids) > 0 {
			if err := tx.Audit(receipt.Actor, "control.receipt_evidence", receipt.DefinitionID, receipt.DownlinkID, map[string]any{"receipt_source": envelope.ReceiptSource, "evidence_ids": ids, "evidence": envelope.CommandEvidence}); err != nil {
				return err
			}
		}
		// Older receipts can add immutable feedback while the current execution and
		// source cursor retain their newer state. Evidence conflicts still reject.
		if previous.Fence > receipt.Fence {
			return nil
		}
		cursorID := node + ":" + receipt.DownlinkID
		if current, err := tx.Get("receipt_cursor", cursorID); err == nil {
			version, err := store.Decode[int64](current)
			if err != nil {
				return err
			}
			if version >= receipt.Version {
				return nil
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		sourceVersion := receipt.Version
		if err := control.ApplyTransition(tx, &receipt, old.Version, "receipt", control.TransitionOptions{Actor: receipt.Actor, Reason: receipt.Reason, Source: "cloud_receipt:" + node, SourceVersion: sourceVersion, EvidenceIDs: ids}); err != nil {
			return err
		}
		return tx.SetEphemeral("receipt_cursor", cursorID, sourceVersion)
	})
}
