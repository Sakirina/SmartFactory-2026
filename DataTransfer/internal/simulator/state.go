// Package simulator implements the five contest scenes using actual protocol
// servers backed by one durable device state.
package simulator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type State struct {
	mu      sync.Mutex
	DB      *sql.DB
	Devices map[string]map[string]any
	Changes int64
}

func OpenState(path string) (*State, error) {
	return openState(path, map[string]map[string]any{
		"climate-1": {"temperature": 26.0, "humidity": 55.0, "fan": false, "heater": false, "humidifier": false, "dehumidifier": false, "interlock": false},
		"agv-1":     {"distance": 150.0, "stopped": false, "interlock": false},
		"light-1":   {"presence": false, "light": false, "interlock": false},
		"gas-1":     {"smoke": 0.0, "combustible": 0.0, "co": 0.0, "extractor": false, "interlock": false},
		"counter-1": {"total": 0.0, "enabled": true, "interlock": false},
	})
}
func openState(path string, initial map[string]map[string]any) (*State, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	s := &State{DB: db, Devices: initial}
	for _, q := range []string{"PRAGMA journal_mode=WAL", "PRAGMA synchronous=FULL", "CREATE TABLE IF NOT EXISTS state(id TEXT PRIMARY KEY,data TEXT NOT NULL)", "CREATE TABLE IF NOT EXISTS commands(id TEXT PRIMARY KEY,body TEXT NOT NULL,result TEXT NOT NULL,received_ns BIGINT NOT NULL)", "CREATE TABLE IF NOT EXISTS pending_events(id TEXT PRIMARY KEY,device_id TEXT NOT NULL,topic TEXT NOT NULL,payload TEXT NOT NULL)"} {
		if _, e = db.Exec(q); e != nil {
			db.Close()
			return nil, e
		}
	}
	var raw string
	e = db.QueryRow("SELECT data FROM state WHERE id='devices'").Scan(&raw)
	if e == nil {
		e = json.Unmarshal([]byte(raw), &s.Devices)
	} else if errors.Is(e, sql.ErrNoRows) {
		e = s.persist()
	}
	if e != nil {
		db.Close()
		return nil, e
	}
	for _, field := range []string{"heater", "humidifier", "dehumidifier"} {
		if _, exists := s.Devices["climate-1"][field]; s.Devices["climate-1"] != nil && !exists {
			s.Devices["climate-1"][field] = false
		}
	}
	return s, nil
}
func (s *State) persist() error {
	b, e := json.Marshal(s.Devices)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec("INSERT INTO state(id,data) VALUES('devices',?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", string(b))
	return e
}
func (s *State) Snapshot() map[string]map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, _ := json.Marshal(s.Devices)
	out := map[string]map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}
func (s *State) Set(device, key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	fields, ok := s.Devices[device]
	if !ok {
		return errors.New("unknown simulated device")
	}
	if _, ok = fields[key]; !ok {
		return errors.New("unknown simulated field")
	}
	switch fields[key].(type) {
	case bool:
		if _, ok := value.(bool); !ok {
			return errors.New("boolean required")
		}
	case float64:
		if _, ok := value.(float64); !ok {
			return errors.New("number required")
		}
	}
	beforeValue := fields[key]
	fields[key] = value
	if e := s.persist(); e != nil {
		fields[key] = beforeValue
		return e
	}
	s.Changes++
	return nil
}
func (s *State) Tick() {
	s.mu.Lock()
	defer s.mu.Unlock()
	climate := s.Devices["climate-1"]
	if climate["fan"] == true && climate["temperature"].(float64) > 22 {
		climate["temperature"] = climate["temperature"].(float64) - 0.15
	}
	if climate["heater"] == true && climate["temperature"].(float64) < 24 {
		climate["temperature"] = climate["temperature"].(float64) + 0.15
	}
	if climate["humidifier"] == true && climate["humidity"].(float64) < 60 {
		climate["humidity"] = climate["humidity"].(float64) + 0.5
	}
	if climate["dehumidifier"] == true && climate["humidity"].(float64) > 40 {
		climate["humidity"] = climate["humidity"].(float64) - 0.5
	}
	gas := s.Devices["gas-1"]
	if gas["extractor"] == true {
		for _, key := range []string{"smoke", "combustible", "co"} {
			v := gas[key].(float64) * 0.9
			if v < 0.01 {
				v = 0
			}
			gas[key] = v
		}
	}
}

type Command struct {
	ID         string            `json:"command_id"`
	Action     string            `json:"action"`
	Params     map[string]string `json:"params"`
	DeadlineMS int64             `json:"start_deadline_ms"`
}

func (s *State) Execute(device string, command Command) (map[string]any, error) {
	return s.execute(device, command, "")
}
func (s *State) ExecuteWithReply(device string, command Command) (map[string]any, error) {
	return s.execute(device, command, "devices/"+device+"/cmd-response")
}
func (s *State) execute(device string, command Command, replyTopic string) (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if command.ID == "" {
		return nil, errors.New("command_id required")
	}
	body, _ := json.Marshal([]any{device, command})
	var previous, result string
	e := s.DB.QueryRow("SELECT body,result FROM commands WHERE id=?", command.ID).Scan(&previous, &result)
	if e == nil {
		if previous != string(body) {
			return nil, errors.New("conflicting command content")
		}
		var out map[string]any
		e = json.Unmarshal([]byte(result), &out)
		if e == nil && replyTopic != "" {
			_, e = s.DB.Exec("INSERT INTO pending_events(id,device_id,topic,payload) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", out["message_id"], device, replyTopic, result)
		}
		return out, e
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	fields, exists := s.Devices[device]
	if !exists {
		return nil, errors.New("unknown device")
	}
	key := ""
	switch command.Action {
	case "set_light":
		key = "light"
	case "set_fan":
		key = "fan"
	case "set_heater":
		key = "heater"
	case "set_humidifier":
		key = "humidifier"
	case "set_dehumidifier":
		key = "dehumidifier"
	case "set_extractor":
		key = "extractor"
	case "stop", "resume":
		key = "stopped"
	case "set_enabled":
		key = "enabled"
	}
	status, message := "SUCCESS", "device state updated"
	var value bool
	if command.DeadlineMS > 0 && time.Now().UnixMilli() >= command.DeadlineMS {
		status, message = "REJECTED", "start deadline expired"
	} else if fields["interlock"] == true {
		status, message = "REJECTED", "physical interlock is active"
	} else if _, ok := fields[key]; !ok {
		status, message = "REJECTED", "unsupported action"
	} else {
		text := command.Params["value"]
		if command.Action == "stop" {
			text = "true"
		}
		if command.Action == "resume" {
			text = "false"
		}
		value, e = strconv.ParseBool(text)
		if e != nil {
			status, message = "REJECTED", "boolean value required"
		}
	}
	out := map[string]any{"command_id": command.ID, "message_id": "response:" + command.ID, "timestamp": time.Now().UnixMilli(), "status": status, "message": message, "result": map[string]string{"device_id": device, "field": key, "received_ns": fmt.Sprint(time.Now().UnixNano())}}
	raw, _ := json.Marshal(out)
	tx, e := s.DB.BeginTx(context.Background(), nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	beforeValue := fields[key]
	committed := false
	defer func() {
		if !committed && status == "SUCCESS" {
			fields[key] = beforeValue
		}
	}()
	if status == "SUCCESS" {
		fields[key] = value
	}
	state, _ := json.Marshal(s.Devices)
	if _, e = tx.Exec("INSERT INTO commands(id,body,result,received_ns) VALUES(?,?,?,?)", command.ID, string(body), string(raw), time.Now().UnixNano()); e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE state SET data=? WHERE id='devices'", string(state)); e != nil {
		return nil, e
	}
	if replyTopic != "" {
		if _, e = tx.Exec("INSERT INTO pending_events(id,device_id,topic,payload) VALUES(?,?,?,?)", out["message_id"], device, replyTopic, string(raw)); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	committed = true
	s.Changes++
	return out, nil
}
func (s *State) NextPulse() (map[string]any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Devices["counter-1"]["enabled"] != true {
		return nil, nil
	}
	total := s.Devices["counter-1"]["total"].(float64) + 1
	payload := map[string]any{"message_id": fmt.Sprintf("pulse:%.0f", total), "source_sequence": int64(total), "timestamp": time.Now().UnixMilli(), "type": "photoelectric_count", "data": map[string]string{"pulse": "1", "total": fmt.Sprintf("%.0f", total)}}
	tx, e := s.DB.BeginTx(context.Background(), nil)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	previous := s.Devices["counter-1"]["total"]
	s.Devices["counter-1"]["total"] = total
	committed := false
	defer func() {
		if !committed {
			s.Devices["counter-1"]["total"] = previous
		}
	}()
	raw, _ := json.Marshal(payload)
	devices, _ := json.Marshal(s.Devices)
	if _, e = tx.Exec("INSERT INTO pending_events(id,device_id,topic,payload) VALUES(?,?,?,?)", payload["message_id"], "counter-1", "devices/counter-1/event", string(raw)); e != nil {
		return nil, e
	}
	if _, e = tx.Exec("UPDATE state SET data=? WHERE id='devices'", string(devices)); e != nil {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	committed = true
	return payload, nil
}

type PendingEvent struct {
	ID, DeviceID, Topic string
	Payload             json.RawMessage
}

func (s *State) Pending(limit int) ([]PendingEvent, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	rows, e := s.DB.Query("SELECT id,device_id,topic,payload FROM pending_events ORDER BY rowid LIMIT ?", limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	var events []PendingEvent
	for rows.Next() {
		var event PendingEvent
		var raw string
		if e = rows.Scan(&event.ID, &event.DeviceID, &event.Topic, &raw); e != nil {
			return nil, e
		}
		event.Payload = json.RawMessage(raw)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *State) Acknowledge(device, id string) error {
	_, e := s.DB.Exec("DELETE FROM pending_events WHERE id=? AND device_id=?", id, device)
	return e
}
