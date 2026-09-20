package notifications

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Message struct {
	ID         string      `json:"id"`
	Channel    string      `json:"channel"`
	Alarm      model.Alarm `json:"alarm"`
	Recipients []string    `json:"recipients"`
	Status     string      `json:"status"`
}
type Delivery struct {
	ID             string      `json:"id"`
	NotificationID string      `json:"notification_id"`
	EntityID       string      `json:"entity_id"`
	UserID         string      `json:"user_id"`
	Channel        string      `json:"channel"`
	Status         string      `json:"status"`
	Reason         string      `json:"reason,omitempty"`
	AtMS           int64       `json:"at_ms"`
	Alarm          model.Alarm `json:"alarm"`
}
type Result struct{ Confirmed, MayHaveSent bool }
type Sender interface {
	Send(context.Context, Delivery, model.User) (Result, error)
}
type Service struct {
	Store    *store.Store
	Identity *identity.Manager
	Sender   Sender
	Edge     bool
}

func (s *Service) recipients(ctx context.Context, m Message) ([]model.User, error) {
	docs, err := s.Store.List(ctx, "user")
	if err != nil {
		return nil, err
	}
	recipients := []model.User{}
	for _, doc := range docs {
		u, err := store.Decode[model.User](doc)
		if err != nil {
			return nil, err
		}
		if !u.Active || u.AI {
			continue
		}
		selected := false
		for _, scope := range m.Recipients {
			if scope == "site" || scope == "user:"+u.ID || scope == u.ID || scope == "department:"+u.DepartmentID {
				selected = true
			}
		}
		if selected && s.Identity.Permit(ctx, identity.Principal{User: u, Actor: model.Actor{UserID: u.ID, Roles: u.Roles}}, "read", m.Alarm.EntityID) == nil {
			recipients = append(recipients, u)
		}
	}
	return recipients, nil
}
func (s *Service) Deliver(ctx context.Context, payload []byte) error {
	var m Message
	if err := store.DecodeJSON(payload, &m); err != nil {
		return err
	}
	if m.ID == "" || m.Alarm.ID == "" || m.Channel == "" {
		return errors.New("notification requires id, channel and alarm")
	}
	users, err := s.recipients(ctx, m)
	if err != nil {
		return err
	}
	for _, u := range users {
		d := Delivery{ID: m.ID + ":" + u.ID, NotificationID: m.ID, EntityID: m.Alarm.EntityID, UserID: u.ID, Channel: m.Channel, Status: "sending", AtMS: s.Store.Now().UnixMilli(), Alarm: m.Alarm}
		send := true
		err = s.Store.Write(ctx, func(tx *store.Tx) error {
			saved, e := tx.Get("notification", d.ID)
			if e == nil {
				old, e := store.Decode[Delivery](saved)
				if e != nil {
					return e
				}
				if old.NotificationID != d.NotificationID || store.Hash(old.Alarm) != store.Hash(d.Alarm) {
					return store.ErrConflict
				}
				if old.Status == "delivered" || old.Status == "result_unknown" || old.Status == "delegated_to_cloud" {
					send = false
					return nil
				}
				if old.Status == "sending" {
					d.Status = "result_unknown"
					d.Reason = "delivery was interrupted before acknowledgement was saved"
					send = false
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if d.Channel == "in_app" {
				d.Status = "delivered"
				send = false
			}
			if s.Edge && d.Channel != "in_app" {
				d.Status = "delegated_to_cloud"
				send = false
			}
			_, e = tx.Put("notification", d.ID, -1, d)
			return e
		})
		if err != nil {
			return err
		}
		if !send {
			continue
		}
		result := Result{}
		if s.Sender == nil {
			err = errors.New("notification channel is not configured")
		} else {
			result, err = s.Sender.Send(ctx, d, u)
		}
		if err == nil && !result.Confirmed && !result.MayHaveSent {
			err = errors.New("notification delivery did not return an acknowledgement")
		}
		d.Status = "pending"
		d.Reason = ""
		if result.Confirmed {
			d.Status = "delivered"
		} else if result.MayHaveSent {
			d.Status = "result_unknown"
		}
		if err != nil {
			d.Reason = err.Error()
		}
		if saveErr := s.Store.Write(ctx, func(tx *store.Tx) error {
			if _, e := tx.Put("notification", d.ID, -1, d); e != nil {
				return e
			}
			return tx.Audit(model.Actor{UserID: "notification-worker", Source: s.Store.NodeID}, "notification."+d.Status, d.EntityID, d.ID, map[string]any{"channel": d.Channel, "recipient_user_id": d.UserID, "status": d.Status, "credential_entry": "notification." + d.Channel})
		}); saveErr != nil {
			return saveErr
		}
		if err != nil && !result.MayHaveSent && !result.Confirmed {
			return err
		}
	}
	if len(users) == 0 {
		_, err = s.Store.Put(ctx, "notification", m.ID, -1, Delivery{ID: m.ID, NotificationID: m.ID, EntityID: m.Alarm.EntityID, Channel: m.Channel, Alarm: m.Alarm, Status: "suppressed", Reason: "no active authorized recipients matched", AtMS: s.Store.Now().UnixMilli()})
	}
	return err
}
func Body(d Delivery) string {
	state := "异常持续中"
	if !d.Alarm.Active {
		state = "异常已恢复"
	}
	return fmt.Sprintf("SmartFactory 告警通知\n设备：%s\n规则：%s\n状态：%s\n等级：%s\n发生时间：%d\n通知标识：%s\n", d.EntityID, d.Alarm.DefinitionID, state, d.Alarm.Severity, d.Alarm.StartedMS, d.ID)
}
func noNewlines(value string) bool { return !strings.ContainsAny(value, "\r\n") }
