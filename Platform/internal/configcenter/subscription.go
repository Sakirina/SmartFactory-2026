package configcenter

import (
	"bufio"
	"bytes"
	"competition2026/product/platform/internal/store"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (s *Service) RegisterInternal(mux *http.ServeMux, token string) {
	authenticated := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if token == "" || subtle.ConstantTimeCompare([]byte(supplied), []byte(token)) != 1 {
				http.Error(w, "service authentication required", 401)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			handler(w, r)
		}
	}
	mux.HandleFunc("GET /internal/config/stream", authenticated(func(w http.ResponseWriter, r *http.Request) {
		flush, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unavailable", 500)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		cursor := ""
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			params, e := s.snapshot(r.Context())
			if e != nil {
				return
			}
			versions := map[string]int64{}
			for _, p := range params {
				versions[p.ID] = p.Version
			}
			next := store.Hash(versions)
			if next != cursor {
				raw, e := json.Marshal(params)
				if e != nil {
					return
				}
				if _, e = fmt.Fprintf(w, "data: %s\n\n", raw); e != nil {
					return
				}
				cursor = next
			} else {
				if _, e = fmt.Fprint(w, ": heartbeat\n\n"); e != nil {
					return
				}
			}
			flush.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
		}
	}))
	mux.HandleFunc("POST /internal/config/ack", authenticated(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			NodeID  string `json:"node_id"`
			ID      string `json:"id"`
			Version int64  `json:"version"`
			Applied bool   `json:"applied"`
			Reason  string `json:"reason"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		if e := decoder.Decode(&request); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		if request.NodeID == "" {
			http.Error(w, "node_id required", 400)
			return
		}
		if e := s.Acknowledge(r.Context(), request.NodeID, request.ID, request.Version, request.Applied, request.Reason); e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		w.WriteHeader(204)
	}))
}
func (s *Service) snapshot(ctx context.Context) ([]Parameter, error) {
	params, e := s.List(ctx)
	if e != nil {
		return nil, e
	}
	for i, p := range params {
		if p.Secret {
			params[i].Value, e = s.Value(ctx, p.ID)
			if e != nil {
				return nil, e
			}
		}
	}
	return params, nil
}

type Subscriber struct {
	URL, Token, NodeID string
	Local              *Service
	Client             *http.Client
	OnApply            func(context.Context) error
	seen               map[string]int64
	started            bool
}

func (c *Subscriber) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c *Subscriber) Apply(ctx context.Context, params []Parameter) error {
	if c.seen == nil {
		c.seen = map[string]int64{}
	}
	for _, p := range params {
		semantic := p
		semantic.Effective = nil
		semantic.Applications = nil
		semantic.State = ""
		digest := store.Hash(semantic)
		receiptID := fmt.Sprintf("%s:%d", p.ID, p.Version)
		if receipt, err := c.Local.Store.Get(ctx, "parameter_receipt", receiptID); err == nil {
			previous, err := store.Decode[struct {
				Hash string `json:"hash"`
			}](receipt)
			if err != nil {
				return err
			}
			if previous.Hash != digest {
				if err = c.ack(ctx, p.ID, p.Version, false, "same configuration version contains different content"); err != nil {
					return err
				}
				continue
			}
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if c.seen[p.ID] >= p.Version {
			continue
		}
		applied, reason := true, ""
		previous, previousErr := c.Local.Store.Get(ctx, "parameter", p.ID)
		if previousErr != nil && !errors.Is(previousErr, store.ErrNotFound) {
			return previousErr
		}
		if previousErr == nil {
			old, e := store.Decode[Parameter](previous)
			if e != nil {
				return e
			}
			if old.Version > p.Version {
				applied, reason = false, "obsolete configuration version"
			}
		}
		if applied && !p.Dynamic && c.started {
			if old, e := c.Local.Store.Get(ctx, "parameter", p.ID); e == nil {
				previous, e := store.Decode[Parameter](old)
				if e != nil {
					return e
				}
				if previous.Version != p.Version {
					applied = false
					reason = "restart_required"
				}
			}
		}
		if applied {
			if e := errors.Join(Validate(p.Schema, p.Value), validateKnown(p)); e != nil {
				applied = false
				reason = e.Error()
			} else {
				if _, e := c.Local.Store.Get(ctx, "parameter_receipt", receiptID); errors.Is(e, store.ErrNotFound) {
					if _, e = c.Local.Store.Put(ctx, "parameter_receipt", receiptID, 0, map[string]any{"hash": digest}); e != nil {
						return e
					}
				}
				if p.Secret {
					raw, e := json.Marshal(p.Value)
					if e != nil {
						return e
					}
					cipher, e := c.Local.Identity.Encrypt("config:"+p.ID, string(raw))
					if e != nil {
						return e
					}
					p.Value = map[string]any{"ciphertext": cipher}
				}
				if e := c.Local.Store.Write(ctx, func(t *store.Tx) error { return t.SetEphemeral("parameter", p.ID, p) }); e != nil {
					return e
				}
			}
		}
		if applied && c.OnApply != nil {
			if e := c.OnApply(ctx); e != nil {
				applied = false
				reason = e.Error()
				if restoreErr := c.Local.Store.Write(ctx, func(tx *store.Tx) error {
					if previousErr == nil {
						return tx.SetEphemeral("parameter", p.ID, previous.Data)
					}
					_, err := tx.ExecContext(ctx, "DELETE FROM documents WHERE kind='parameter' AND id=$1", p.ID)
					return err
				}); restoreErr != nil {
					return restoreErr
				}
			}
		}
		if e := c.ack(ctx, p.ID, p.Version, applied, reason); e != nil {
			return e
		}
		if applied {
			if e := c.Local.Acknowledge(ctx, c.NodeID, p.ID, p.Version, true, ""); e != nil {
				return e
			}
		}
		if applied || !p.Dynamic || reason == "obsolete configuration version" {
			c.seen[p.ID] = p.Version
		}
	}
	c.started = true
	return nil
}
func (c *Subscriber) ack(ctx context.Context, id string, version int64, applied bool, reason string) error {
	raw, _ := json.Marshal(map[string]any{"node_id": c.NodeID, "id": id, "version": version, "applied": applied, "reason": reason})
	req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(c.URL, "/")+"/internal/config/ack", bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, e := c.client().Do(req.WithContext(call))
	if e != nil {
		return e
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	if response.StatusCode != 204 {
		return fmt.Errorf("configuration acknowledgement returned %d", response.StatusCode)
	}
	return nil
}
func (c *Subscriber) Run(ctx context.Context) error {
	for {
		e := c.listen(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if c.Local != nil {
			_ = c.Local.Store.Write(ctx, func(t *store.Tx) error {
				return t.SetEphemeral("configuration_connection", c.NodeID, map[string]any{"status": "retrying", "error": e.Error(), "at_ms": c.Local.Store.Now().UnixMilli()})
			})
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
	}
}
func (c *Subscriber) listen(ctx context.Context) error {
	request, e := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(c.URL, "/")+"/internal/config/stream", nil)
	if e != nil {
		return e
	}
	request.Header.Set("Authorization", "Bearer "+c.Token)
	response, e := c.client().Do(request)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("configuration subscription returned %d", response.StatusCode)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var last []Parameter
	heartbeats := 0
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			if strings.HasPrefix(line, ": heartbeat") {
				heartbeats++
				if heartbeats%5 == 0 && len(last) > 0 {
					if e = c.Apply(ctx, last); e != nil {
						return e
					}
				}
			}
			continue
		}
		var params []Parameter
		if e = store.DecodeJSON([]byte(strings.TrimPrefix(line, "data: ")), &params); e != nil {
			return e
		}
		if e = c.Apply(ctx, params); e != nil {
			return e
		}
		last = params
		if e = c.Local.Store.Write(ctx, func(t *store.Tx) error {
			return t.SetEphemeral("configuration_connection", c.NodeID, map[string]any{"status": "connected", "at_ms": c.Local.Store.Now().UnixMilli()})
		}); e != nil {
			return e
		}
	}
	if e = scanner.Err(); e != nil {
		return e
	}
	return errors.New("configuration stream ended")
}
