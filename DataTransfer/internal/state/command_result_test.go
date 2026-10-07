package state

import (
	"context"
	"path/filepath"
	"testing"

	dt "competition2026/product/datatransfer/gen/datatransfer/v1"
	"google.golang.org/protobuf/proto"
)

func TestLegacyCommandResultPreservesOriginalDigestAndTime(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	raw, _ := proto.Marshal(&dt.CommandResponsePayload{CommandId: "old-command", Status: dt.CommandStatus_SUCCESS})
	digest := []byte("legacy-digest-preserved")
	if _, err = s.db.ExecContext(ctx, "INSERT INTO commands(command_id,digest,response,phase,updated_at_ms) VALUES(?,?,?,'complete',?)", "old-command", digest, raw, 12345); err != nil {
		t.Fatal(err)
	}
	result, err := s.CommandResult(ctx, "old-command")
	if err != nil || !result.Found || result.BindingKnown || result.RecordedAtMs != 12345 || string(result.RequestSha256) != string(digest) || result.Response.Status != dt.CommandStatus_SUCCESS {
		t.Fatal(result, err)
	}
	var actualTime int64
	var actualDigest []byte
	if err = s.db.QueryRowContext(ctx, "SELECT digest,updated_at_ms FROM commands WHERE command_id='old-command'").Scan(&actualDigest, &actualTime); err != nil {
		t.Fatal(err)
	}
	if actualTime != 12345 || string(actualDigest) != string(digest) {
		t.Fatal("readonly legacy query modified journal")
	}
}
