package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"competition2026/product/platform/pkg/model"
)

func (t *Tx) BusinessRequest(scope, id, hash string, value any) (bool, error) {
	if strings.TrimSpace(id) == "" || len(id) > 200 {
		return false, errors.New("request_id must contain 1 to 200 characters")
	}
	if err := t.lock("business-request", scope+"\x00"+id, false); err != nil {
		return false, err
	}
	var previous, response string
	err := t.QueryRowContext(t.Ctx, "SELECT payload_hash,response FROM sf_business_requests WHERE scope=$1 AND request_id=$2", scope, id).Scan(&previous, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if previous != hash {
		return false, ErrConflict
	}
	return true, DecodeJSON([]byte(response), value)
}

func (t *Tx) CompleteBusinessRequest(scope, id, hash string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = t.ExecContext(t.Ctx, "INSERT INTO sf_business_requests(scope,request_id,payload_hash,response,created_ms) VALUES($1,$2,$3,$4,$5)", scope, id, hash, string(raw), t.Store.Now().UnixMilli())
	return err
}

func (t *Tx) NextBusinessSequence() (int64, error) {
	if err := t.lock("business-source", t.Store.NodeID, false); err != nil {
		return 0, err
	}
	var n int64
	err := t.QueryRowContext(t.Ctx, "INSERT INTO sf_business_source_sequence(source_id,sequence) VALUES($1,1) ON CONFLICT(source_id) DO UPDATE SET sequence=sf_business_source_sequence.sequence+1 RETURNING sequence", t.Store.NodeID).Scan(&n)
	return n, err
}

func EmptyAlarmCase(alarm model.Alarm) model.AlarmCase {
	return model.AlarmCase{ID: alarm.ID, AlarmID: alarm.ID, EntityID: alarm.EntityID, DefinitionID: alarm.DefinitionID, Status: "open", Operations: []model.AlarmOperation{}, ConflictFields: []string{}, PendingBasis: []string{}}
}

func (s *Store) AlarmCase(ctx context.Context, alarm model.Alarm) (model.AlarmCase, error) {
	doc, err := s.Get(ctx, "alarm_case", alarm.ID)
	if errors.Is(err, ErrNotFound) {
		return EmptyAlarmCase(alarm), nil
	}
	if err != nil {
		return model.AlarmCase{}, err
	}
	return Decode[model.AlarmCase](doc)
}

// ApplyAlarmOperation combines immutable causal records. Conflicting concurrent
// responsibility or completion decisions remain visible until a later decision
// names both in its basis; acknowledgement and notes retain every operation.
func (t *Tx) ApplyAlarmOperation(op model.AlarmOperation) (model.AlarmCase, error) {
	var result model.AlarmCase
	if op.ID == "" || op.SourceID == "" || op.SourceSequence < 1 || op.Version != 1 || op.AlarmID == "" || op.Actor.UserID == "" || strings.TrimSpace(op.Reason) == "" || len(op.Reason) > 4000 || len(op.Basis) > 1000 {
		return result, errors.New("invalid alarm operation identity or content")
	}
	switch op.Action {
	case "acknowledge", "assign", "note", "complete", "reopen":
	default:
		return result, errors.New("unknown alarm operation")
	}
	if op.EntityID == "" || op.DefinitionID == "" || op.AtMS <= 0 || (op.Action == "assign" && op.AssigneeID == "") || (op.Action != "assign" && op.AssigneeID != "") {
		return result, errors.New("invalid alarm operation content")
	}
	basis := map[string]bool{}
	for _, id := range op.Basis {
		if id == "" || id == op.ID || basis[id] {
			return result, errors.New("invalid alarm operation basis")
		}
		basis[id] = true
	}
	caseDoc, err := t.Get("alarm_case", op.AlarmID)
	if err == nil {
		result, err = Decode[model.AlarmCase](caseDoc)
	} else if errors.Is(err, ErrNotFound) {
		result = EmptyAlarmCase(model.Alarm{ID: op.AlarmID, EntityID: op.EntityID, DefinitionID: op.DefinitionID})
		err = nil
	}
	if err != nil {
		return result, err
	}
	if result.EntityID != op.EntityID || result.DefinitionID != op.DefinitionID {
		return result, ErrConflict
	}
	if old, err := t.Get("alarm_operation", op.ID); err == nil {
		prior, e := Decode[model.AlarmOperation](old)
		if e != nil {
			return result, e
		}
		if Hash(prior) != Hash(op) {
			return result, ErrConflict
		}
		return result, nil
	} else if !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if len(result.Operations) >= 1000 {
		return result, errors.New("alarm operation limit is 1000")
	}
	if err := t.lock("alarm-operation-source", op.SourceID, false); err != nil {
		return result, err
	}
	var existing string
	err = t.QueryRowContext(t.Ctx, "SELECT operation_id FROM sf_alarm_operation_sources WHERE source_id=$1 AND sequence=$2", op.SourceID, op.SourceSequence).Scan(&existing)
	if err == nil {
		return result, ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if _, err = t.ExecContext(t.Ctx, "INSERT INTO sf_alarm_operation_sources(source_id,sequence,operation_id,payload_hash) VALUES($1,$2,$3,$4)", op.SourceID, op.SourceSequence, op.ID, Hash(op)); err != nil {
		return result, err
	}
	if _, err = t.Put("alarm_operation", op.ID, 0, op); err != nil {
		return result, err
	}
	result.Operations = append(result.Operations, op)
	if err = foldAlarmCase(&result); err != nil {
		return result, err
	}
	result.Version = caseDoc.Version + 1
	if _, err = t.Put("alarm_case", op.AlarmID, caseDoc.Version, result); err != nil {
		return result, err
	}
	if result.Acknowledged {
		if err = t.AcknowledgeAlarm(op.AlarmID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func foldAlarmCase(c *model.AlarmCase) error {
	sort.Slice(c.Operations, func(i, j int) bool {
		a, b := c.Operations[i], c.Operations[j]
		if a.AtMS != b.AtMS {
			return a.AtMS < b.AtMS
		}
		return a.ID < b.ID
	})
	known := map[string]model.AlarmOperation{}
	for _, op := range c.Operations {
		known[op.ID] = op
	}
	valid := map[string]bool{}
	visiting := map[string]bool{}
	missing := map[string]bool{}
	var visit func(string) (bool, error)
	visit = func(id string) (bool, error) {
		if v, ok := valid[id]; ok {
			return v, nil
		}
		op, ok := known[id]
		if !ok {
			missing[id] = true
			return false, nil
		}
		if visiting[id] {
			return false, errors.New("alarm operation basis contains a cycle")
		}
		visiting[id] = true
		ready := true
		for _, b := range op.Basis {
			v, e := visit(b)
			if e != nil {
				return false, e
			}
			ready = ready && v
		}
		delete(visiting, id)
		valid[id] = ready
		return ready, nil
	}
	for _, op := range c.Operations {
		if _, err := visit(op.ID); err != nil {
			return err
		}
	}
	c.Acknowledged = false
	c.AcknowledgedBy = ""
	c.AcknowledgedMS = 0
	c.AssigneeID = ""
	c.Status = "open"
	c.CompletedMS = 0
	c.UpdatedMS = 0
	c.ConflictFields = []string{}
	c.PendingBasis = []string{}
	for id := range missing {
		c.PendingBasis = append(c.PendingBasis, id)
	}
	sort.Strings(c.PendingBasis)
	var assigns, states []model.AlarmOperation
	for _, op := range c.Operations {
		c.UpdatedMS = max(c.UpdatedMS, op.AtMS)
		if !valid[op.ID] {
			continue
		}
		switch op.Action {
		case "acknowledge":
			if !c.Acknowledged {
				c.Acknowledged = true
				c.AcknowledgedBy = op.Actor.UserID
				c.AcknowledgedMS = op.AtMS
			}
		case "assign":
			assigns = append(assigns, op)
		case "complete", "reopen":
			states = append(states, op)
		}
	}
	maximal := func(ops []model.AlarmOperation) []model.AlarmOperation {
		out := []model.AlarmOperation{}
		for _, a := range ops {
			covered := false
			for _, b := range ops {
				for _, id := range b.Basis {
					covered = covered || id == a.ID
				}
			}
			if !covered {
				out = append(out, a)
			}
		}
		return out
	}
	assigns = maximal(assigns)
	if len(assigns) > 0 {
		c.AssigneeID = assigns[len(assigns)-1].AssigneeID
		for _, op := range assigns {
			if op.AssigneeID != c.AssigneeID {
				c.ConflictFields = append(c.ConflictFields, "assignee_id")
				c.AssigneeID = ""
				break
			}
		}
	}
	states = maximal(states)
	if len(states) > 0 {
		last := states[len(states)-1]
		if last.Action == "complete" {
			c.Status = "completed"
			c.CompletedMS = last.AtMS
		}
		for _, op := range states {
			if op.Action != last.Action {
				c.ConflictFields = append(c.ConflictFields, "status")
				c.Status = "open"
				c.CompletedMS = 0
				break
			}
		}
	}
	if len(c.ConflictFields) > 0 {
		c.Status = "conflict"
	}
	return nil
}

func (t *Tx) AcknowledgeAlarm(id string) error {
	doc, err := t.Get("alarm", id)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	alarm, err := Decode[model.Alarm](doc)
	if err != nil {
		return err
	}
	if alarm.Acknowledged {
		return nil
	}
	alarm.Acknowledged = true
	alarm.Version++
	if err = t.SetEphemeral("alarm", id, alarm); err != nil {
		return err
	}
	activeID := alarm.DefinitionID + ":" + alarm.EntityID
	if d, e := t.Get("active_alarm", activeID); e == nil {
		a, e := Decode[model.Alarm](d)
		if e != nil {
			return e
		}
		if a.ID == alarm.ID {
			if e = t.SetEphemeral("active_alarm", activeID, alarm); e != nil {
				return e
			}
		}
	} else if !errors.Is(e, ErrNotFound) {
		return e
	}
	return t.Enqueue(fmt.Sprintf("tb-alarm:%s:%d", alarm.ID, alarm.Version), "tb_alarm", alarm.EntityID, alarm)
}
