package control

import (
	"context"
	"errors"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Service) leaderEligible(ctx context.Context, leader model.User, req model.Execution) error {
	if leader.DepartmentID == "" {
		return errors.New("leader requires a current organization assignment")
	}
	doc, err := s.Store.Get(ctx, "user", req.Actor.UserID)
	if err != nil {
		return err
	}
	member, err := store.Decode[model.User](doc)
	if err != nil {
		return err
	}
	if !member.Active {
		return errors.New("request initiator is no longer active")
	}
	for id, depth := member.ID, 0; id != "" && depth < 64; depth++ {
		current, err := s.Store.Get(ctx, "user", id)
		if err != nil {
			return err
		}
		person, err := store.Decode[model.User](current)
		if err != nil {
			return err
		}
		if person.ManagerID == leader.ID {
			return nil
		}
		id = person.ManagerID
	}
	for id, depth := member.DepartmentID, 0; id != "" && depth < 64; depth++ {
		if id == leader.DepartmentID {
			return nil
		}
		department, err := s.Store.Get(ctx, "department", id)
		if errors.Is(err, store.ErrNotFound) {
			break
		}
		if err != nil {
			return err
		}
		var value struct {
			ParentID  string `json:"parent_id"`
			ManagerID string `json:"manager_id"`
		}
		if err = store.DecodeJSON(department.Data, &value); err != nil {
			return err
		}
		if value.ManagerID == leader.ID {
			return nil
		}
		id = value.ParentID
	}
	return errors.New("leader is outside the initiator's current organization chain")
}
