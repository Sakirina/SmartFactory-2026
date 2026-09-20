package store

import (
	"competition2026/product/platform/pkg/model"
	"context"
	"net/url"
	"os"
	"testing"
	"time"
)

func TestPostgresRetentionDropsExpiredPartitionBeforeRowDeletion(t *testing.T) {
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("set SF_TEST_POSTGRES_DATABASE to an isolated PostgreSQL fixture")
	}
	ctx := context.Background()
	admin, err := Open(ctx, dsn, "retention-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	const schema = "sf_retention_fixture"
	if _, err = admin.DB.Exec("CREATE SCHEMA " + schema); err != nil {
		t.Fatal(err)
	}
	defer admin.DB.Exec("DROP SCHEMA " + schema + " CASCADE")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	s, err := Open(ctx, u.String(), "retention-fixture", make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 9, 20, 1, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	cut := now.AddDate(0, 0, -30)
	if err = s.Write(ctx, func(tx *Tx) error {
		for i, at := range []time.Time{cut.Add(-48 * time.Hour), cut.Add(-time.Hour), cut.Add(time.Hour)} {
			p := model.Observation{ID: []string{"expired", "partial-expired", "retained"}[i], MessageID: "fixture", DeviceID: "device", Key: "value", ObservedMS: at.UnixMilli(), ReceivedMS: at.UnixMilli(), Quality: "GOOD", Revision: 1, Value: i}
			if err := tx.InsertPoint(p); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`CREATE FUNCTION reject_expired_delete() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'expired partition received row deletion'; END $$`); err != nil {
		t.Fatal(err)
	}
	old := "observations_" + cut.Add(-48*time.Hour).Format("20060102")
	if _, err = s.DB.Exec("CREATE TRIGGER reject_row_delete BEFORE DELETE ON " + old + " FOR EACH ROW EXECUTE FUNCTION reject_expired_delete()"); err != nil {
		t.Fatal(err)
	}
	counts, err := s.ApplyRetention(ctx, DefaultRetention())
	if err != nil || counts["partitions"] != 1 || counts["raw"] != 1 {
		t.Fatal(counts, err)
	}
	var id string
	if err = s.DB.QueryRow("SELECT id FROM observations").Scan(&id); err != nil || id != "retained" {
		t.Fatal(id, err)
	}
}
