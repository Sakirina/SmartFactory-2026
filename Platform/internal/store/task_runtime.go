package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"competition2026/product/platform/pkg/model"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver"
	"github.com/riverqueue/river/rivertype"
)

func (s *Store) CurrentTime() time.Time { return s.Now() }

// NewTaskWorkerClient keeps official SQL-driver selection in the store adapter.
func (s *Store) NewTaskWorkerClient(config *river.Config) (*river.Client[*sql.Tx], error) {
	return river.NewClient(s.RiverDriver(), config)
}

func (s *Store) RecordRecomputeFailure(ctx context.Context, taskID, businessID string, cause error) error {
	return s.Write(ctx, func(tx *Tx) error {
		current, err := tx.Get("job", businessID)
		if err != nil {
			return err
		}
		latest, err := Decode[model.Job](current)
		if err != nil {
			return err
		}
		if latest.TaskID != taskID || latest.Status == "cancelled" || latest.Status == "pending" {
			return nil
		}
		latest.Status, latest.Error, latest.Version = "failed", cause.Error(), current.Version+1
		_, err = tx.Put("job", businessID, current.Version, latest)
		return err
	})
}

func (s *Store) CompleteProjection(ctx context.Context, deliveryID string, riverID int64) error {
	return s.Write(ctx, func(tx *Tx) error {
		if _, err := tx.ExecContext(ctx, "DELETE FROM outbox WHERE id=$1", deliveryID); err != nil {
			return err
		}
		// The worker obtained this running attempt from River. The official
		// completion API verifies its actual row state within this transaction.
		job := &river.Job[TaskArgs]{JobRow: &rivertype.JobRow{ID: riverID, State: rivertype.JobStateRunning}}
		_, err := river.JobCompleteTx[riverdriver.Driver[*sql.Tx]](ctx, tx.Tx, job)
		return err
	})
}

func (s *Store) ScheduleArchive(ctx context.Context) error {
	if !s.Policy().Archive.Enabled {
		return nil
	}
	return s.Write(ctx, func(tx *Tx) error {
		if err := tx.LockTaskSchedule(); err != nil {
			return err
		}
		var pending int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM sf_tasks t JOIN river_job r ON r.id=t.river_id WHERE t.kind='archive' AND r.state IN ('available','scheduled','pending','running','retryable')").Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		_, err := tx.EnqueueTask("archive", fmt.Sprint(time.Now().Unix()/5), 0, "", []string{"*"}, time.Time{})
		return err
	})
}
