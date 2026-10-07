package configcenter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func configHTTPError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, identity.ErrAuthentication) {
		status = 401
	} else if errors.Is(err, identity.ErrDenied) {
		status = 403
	} else if errors.Is(err, store.ErrConflict) {
		status = 409
	} else if errors.Is(err, store.ErrNotFound) {
		status = 404
	} else if errors.Is(err, nodeidentity.ErrAuthorityUnavailable) || errors.Is(err, ErrConnectorSourceUnavailable) {
		status = 503
	}
	// Never serialize consumer errors or decrypted payloads into HTTP/log evidence.
	http.Error(w, http.StatusText(status), status)
}

func secureInternal(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func configDecode(w http.ResponseWriter, r *http.Request, v any) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("one JSON object required")
	}
	return requireConfigFields(raw, reflect.TypeOf(v))
}

func requireConfigFields(raw []byte, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		if string(raw) == "null" {
			return nil
		}
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array {
		var entries []json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := requireConfigFields(entry, typ.Elem()); err != nil {
				return err
			}
		}
		return nil
	}
	if typ.Kind() != reflect.Struct {
		return nil
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return err
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		value, exists := values[name]
		if field.Tag.Get("required") == "true" && (!exists || string(value) == "null") {
			return errors.New("required configuration field is missing")
		}
		if exists {
			if err := requireConfigFields(value, field.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) RegisterWorkloads(mux *http.ServeMux) {
	s.RegisterConnectorSource(mux)
	auth := func(fn func(http.ResponseWriter, *http.Request, nodeidentity.Principal)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			if !secureInternal(r) {
				configHTTPError(w, identity.ErrDenied)
				return
			}
			p, err := s.workloadService().FromRequest(r)
			if err != nil {
				configHTTPError(w, err)
				return
			}
			fn(w, r, p)
		}
	}
	mux.HandleFunc("POST /internal/workloads/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !secureInternal(r) {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		var in struct {
			InstanceID string `json:"instance_id"`
		}
		if err := configDecode(w, r, &in); err != nil {
			configHTTPError(w, err)
			return
		}
		p, err := s.workloadService().ActivateInstance(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), in.InstanceID)
		if err != nil {
			configHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.Identity)
	})
	mux.HandleFunc("POST /internal/config/v2/versions", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) {
		var in struct {
			References []model.ConfigurationReference `json:"references"`
		}
		if err := configDecode(w, r, &in); err != nil {
			configHTTPError(w, err)
			return
		}
		if in.References == nil {
			configHTTPError(w, errors.New("references required"))
			return
		}
		out, err := s.GetReleaseConfiguration(r.Context(), p, in.References)
		if err != nil {
			configHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("POST /internal/config/v2/metadata", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS != nil {
			if len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "config-client-cloud-1" {
				configHTTPError(w, identity.ErrDenied)
				return
			}
		} else if !secureInternal(r) {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		var in struct {
			References []model.ConfigurationReference `json:"references"`
		}
		if e := configDecode(w, r, &in); e != nil {
			configHTTPError(w, e)
			return
		}
		if in.References == nil || len(in.References) > 512 {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		out, e := s.ConfigurationMetadata(r.Context(), in.References)
		if e != nil {
			configHTTPError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
	mux.HandleFunc("POST /internal/config/v2/release-target", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) {
		var in model.ReleaseConfigurationTarget
		if e := configDecode(w, r, &in); e != nil {
			configHTTPError(w, e)
			return
		}
		if in.DeploymentID == "" || in.Generation < 1 || in.References == nil {
			configHTTPError(w, errors.New("complete release target required"))
			return
		}
		service := s.workloadService()
		if service.AuthorityURL == "" {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		var proof model.ReleaseConfigurationTargetProof
		if e := service.AuthorityCall(r.Context(), "POST", "/internal/authority/release-target", p.CredentialToken(), p.InstanceID, in, &proof); e != nil {
			configHTTPError(w, e)
			return
		}
		if store.Hash(proof.Identity) != store.Hash(p.Identity) || proof.DeploymentID != in.DeploymentID || proof.Generation != in.Generation || !sameReferences(proof.References, in.References) {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		if e := s.EnsureConnectorVersions(r.Context(), in.References); e != nil {
			configHTTPError(w, e)
			return
		}
		err := s.Store.Write(r.Context(), func(tx *store.Tx) error {
			if _, e := service.ValidateTx(tx, p, "release"); e != nil {
				return e
			}
			old, e := tx.Get("configuration_release_assignment", p.Identity.ID)
			if e == nil {
				var previous model.ReleaseConfigurationTarget
				if e = store.DecodeJSON(old.Data, &previous); e != nil {
					return e
				}
				if previous.Generation > in.Generation {
					return store.ErrConflict
				}
				if previous.Generation == in.Generation && (previous.DeploymentID != in.DeploymentID || !sameReferences(previous.References, in.References)) {
					return store.ErrConflict
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if e = s.SetReleaseConfigurationTargetTx(tx, p, in.References); e != nil {
				return e
			}
			return tx.SetEphemeral("configuration_release_assignment", p.Identity.ID, in)
		})
		if err != nil {
			configHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(proof)
	}))
	mux.HandleFunc("POST /internal/config/v2/credentials/resolve", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) {
		var in model.CredentialResolution
		if err := configDecode(w, r, &in); err != nil {
			configHTTPError(w, err)
			return
		}
		out, err := s.ResolveCredential(r.Context(), p, in)
		if err != nil {
			configHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("POST /internal/config/v2/reports", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) {
		var in model.ConfigurationReport
		if err := configDecode(w, r, &in); err != nil {
			configHTTPError(w, err)
			return
		}
		out, err := s.ReportConfiguration(r.Context(), p, in)
		if err != nil {
			configHTTPError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	}))
	mux.HandleFunc("GET /internal/config/v2/stream", auth(func(w http.ResponseWriter, r *http.Request, p nodeidentity.Principal) {
		out, err := s.WorkloadSnapshot(r.Context(), p)
		if err != nil {
			configHTTPError(w, err)
			return
		}
		controller := http.NewResponseController(w)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Accel-Buffering", "no")
		send := func(event string, data []byte) error {
			if e := controller.SetWriteDeadline(time.Now().Add(3 * time.Second)); e != nil && !errors.Is(e, http.ErrNotSupported) {
				return e
			}
			defer controller.SetWriteDeadline(time.Time{})
			if event == "" {
				if _, e := fmt.Fprint(w, ": heartbeat\n\n"); e != nil {
					return e
				}
			} else if _, e := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); e != nil {
				return e
			}
			return controller.Flush()
		}
		cursor := ""
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			next := store.Hash(out)
			if next != cursor {
				raw, e := json.Marshal(out)
				if e != nil {
					return
				}
				if e = send("configuration", raw); e != nil {
					return
				}
				cursor = next
			} else {
				if e := send("", nil); e != nil {
					return
				}
			}
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
			}
			// The established stream retains its authenticated revision. Revocation,
			// rotation and scope changes terminate it before another data event.
			out, err = s.WorkloadSnapshot(r.Context(), p)
			if err != nil {
				event := "authority_wait"
				if errors.Is(err, identity.ErrAuthentication) || errors.Is(err, identity.ErrDenied) || errors.Is(err, store.ErrConflict) {
					event = "identity_changed"
				}
				_ = send(event, []byte("{}"))
				return
			}
		}
	}))
}

func sameReferences(a, b []model.ConfigurationReference) bool {
	left := slices.Clone(a)
	right := slices.Clone(b)
	cmp := func(x, y model.ConfigurationReference) int { return strings.Compare(x.Kind+":"+x.ID, y.Kind+":"+y.ID) }
	slices.SortFunc(left, cmp)
	slices.SortFunc(right, cmp)
	return slices.Equal(left, right)
}

func (s *Service) CheckLegacySubscription(ctx context.Context) error {
	if s.Workloads != nil && !s.LegacySubscription {
		return identity.ErrDenied
	}
	return nil
}
