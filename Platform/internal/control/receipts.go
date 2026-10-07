package control

import (
	"errors"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type operationReceiptCursor struct {
	Version int64  `json:"version"`
	Hash    string `json:"hash"`
}

// ReceiveOperationReceipt preserves source and local document revisions as
// separate sequences, just as execution receipts do.
func ReceiveOperationReceipt(tx *store.Tx, node string, receipt model.ControlOperation) error {
	started := receipt.Status == "pending" && receipt.StartedMS > 0 && receipt.ProcessedMS == 0 && receipt.Result == nil
	if receipt.ID == "" || receipt.TargetNodeID != node || receipt.Version < 1 || !(operationFinished(receipt.Status) || started) {
		return errors.New("invalid control operation receipt")
	}
	doc, err := tx.Get("control_operation", receipt.ID)
	if err != nil {
		return err
	}
	previous, err := store.Decode[model.ControlOperation](doc)
	if err != nil {
		return err
	}
	if previous.RequestHash != receipt.RequestHash || previous.TargetNodeID != node || previous.ExecutionID != receipt.ExecutionID || previous.Action != receipt.Action || previous.Actor.UserID != receipt.Actor.UserID || previous.ExpectedVersion != receipt.ExpectedVersion || previous.ExpectedSourceVersion != receipt.ExpectedSourceVersion || previous.DeadlineMS != receipt.DeadlineMS {
		return store.ErrConflict
	}
	if !started && receipt.ProcessedMS < previous.RequestedMS-5000 || (started || receipt.Status == "completed") && (receipt.StartedMS < previous.RequestedMS-5000 || receipt.StartedMS >= receipt.DeadlineMS || !started && receipt.ProcessedMS < receipt.StartedMS) {
		return errors.New("operation receipt time does not match its request")
	}
	if receipt.Result != nil && (receipt.Result.DownlinkID != receipt.ExecutionID || receipt.Result.Version != receipt.ResultVersion) {
		return store.ErrConflict
	}
	cursorID := node + ":" + receipt.ID
	source := operationReceiptCursor{Version: receipt.Version, Hash: store.Hash(receipt)}
	if old, err := tx.Get("operation_receipt_cursor", cursorID); err == nil {
		cursor, err := store.Decode[operationReceiptCursor](old)
		if err != nil {
			return err
		}
		if cursor.Version > source.Version {
			return nil
		}
		if cursor.Version == source.Version {
			if cursor.Hash != source.Hash {
				return store.ErrConflict
			}
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if operationFinished(previous.Status) && previous.Status != receipt.Status {
		return store.ErrConflict
	}
	receipt.Version = doc.Version + 1
	if _, err = tx.Put("control_operation", receipt.ID, doc.Version, receipt); err != nil {
		return err
	}
	if err = tx.SetEphemeral("operation_receipt_cursor", cursorID, source); err != nil {
		return err
	}
	return tx.Audit(receipt.Actor, "control.operation.receipt", receipt.ExecutionID, receipt.ExecutionID, receipt)
}
