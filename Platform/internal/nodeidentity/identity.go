package nodeidentity

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type Service struct {
	Store        *store.Store
	Cipher       *identity.Manager
	AuthorityURL string
	HTTPClient   *http.Client
}

type Principal struct {
	Identity        model.WorkloadIdentity
	Generation      int64
	InstanceID      string
	InstanceEpoch   int64
	credentialHash  string
	credentialToken string
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func (s *Service) Lookup(ctx context.Context, id string) (model.WorkloadIdentity, error) {
	if s.AuthorityURL != "" {
		return s.remoteLookup(ctx, id)
	}
	d, err := s.Store.Get(ctx, "workload_identity", id)
	if err != nil {
		return model.WorkloadIdentity{}, err
	}
	return store.Decode[model.WorkloadIdentity](d)
}

func (s *Service) LookupTx(tx *store.Tx, id string) (model.WorkloadIdentity, error) {
	if s.AuthorityURL != "" {
		return s.remoteLookup(tx.Ctx, id)
	}
	d, err := tx.Read("workload_identity", id)
	if err != nil {
		return model.WorkloadIdentity{}, err
	}
	return store.Decode[model.WorkloadIdentity](d)
}

func validateIdentity(w model.WorkloadIdentity) error {
	if w.ID == "" || w.NodeID == "" || w.Program == "" || w.Purpose == "" || len(w.ID) > 128 || len(w.NodeID) > 128 || len(w.Program) > 64 || len(w.Purpose) > 64 || strings.ContainsAny(w.ID, " \t\n\r") {
		return errors.New("identity, node, program and purpose are required")
	}
	for _, list := range [][]string{w.Capabilities, w.ParameterIDs, w.ConnectorIDs} {
		if len(list) > 256 {
			return errors.New("workload scope exceeds 256 entries")
		}
		for _, item := range list {
			if item == "" || item == "*" {
				return errors.New("explicit workload scope entries are required")
			}
		}
	}
	return nil
}

// PutTx updates public metadata and leaves the credential generation unchanged.
func (s *Service) PutTx(tx *store.Tx, actor model.Actor, w model.WorkloadIdentity, expected int64) (model.WorkloadIdentity, error) {
	if err := validateIdentity(w); err != nil {
		return w, err
	}
	if expected < 0 {
		return w, store.ErrConflict
	}
	old, err := tx.Get("workload_identity", w.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return w, err
	}
	if old.Version != expected {
		return w, store.ErrConflict
	}
	if err == nil {
		previous, e := store.Decode[model.WorkloadIdentity](old)
		if e != nil {
			return w, e
		}
		w.Generation = previous.Generation
		w.InstanceID = previous.InstanceID
		w.InstanceEpoch = previous.InstanceEpoch
	} else {
		w.Generation = 0
		w.InstanceID = ""
		w.InstanceEpoch = 0
	}
	w.Version = expected + 1
	w.UpdatedMS = s.Store.Now().UnixMilli()
	if _, err = tx.Put("workload_identity", w.ID, expected, w); err != nil {
		return w, err
	}
	return w, tx.Audit(actor, "workload.identity.update", w.NodeID, "", w)
}

func (s *Service) Put(ctx context.Context, actor model.Actor, w model.WorkloadIdentity, expected int64) (model.WorkloadIdentity, error) {
	err := s.Store.Write(ctx, func(tx *store.Tx) error { var e error; w, e = s.PutTx(tx, actor, w, expected); return e })
	return w, err
}

// RotateTx accepts a caller-generated high-entropy token; it never stores plaintext.
func (s *Service) RotateTx(tx *store.Tx, actor model.Actor, id, token string, expected int64) (model.WorkloadIdentity, error) {
	if len(token) < 32 || len(token) > 512 {
		return model.WorkloadIdentity{}, errors.New("credential must contain 32 to 512 characters")
	}
	d, err := tx.Get("workload_identity", id)
	if err != nil {
		return model.WorkloadIdentity{}, err
	}
	w, err := store.Decode[model.WorkloadIdentity](d)
	if err != nil {
		return w, err
	}
	if d.Version != expected {
		return w, store.ErrConflict
	}
	w.Generation++
	w.Version = d.Version + 1
	w.InstanceID = ""
	w.InstanceEpoch++
	w.UpdatedMS = s.Store.Now().UnixMilli()
	// Locking the identity before its credential serializes rotation and reports.
	_, err = tx.ExecContext(tx.Ctx, `INSERT INTO sf_workload_credentials(identity_id,generation,token_hash) VALUES($1,$2,$3) ON CONFLICT(identity_id) DO UPDATE SET generation=excluded.generation,token_hash=excluded.token_hash`, id, w.Generation, hashToken(token))
	if err != nil {
		return w, err
	}
	if _, err = tx.Put("workload_identity", id, d.Version, w); err != nil {
		return w, err
	}
	return w, tx.Audit(actor, "workload.credential.rotate", w.NodeID, "", map[string]any{"identity_id": id, "generation": w.Generation})
}

func (s *Service) Rotate(ctx context.Context, actor model.Actor, id, token string, expected int64) (model.WorkloadIdentity, error) {
	var w model.WorkloadIdentity
	err := s.Store.Write(ctx, func(tx *store.Tx) error { var e error; w, e = s.RotateTx(tx, actor, id, token, expected); return e })
	return w, err
}

func (s *Service) credential(ctx context.Context, token string) (string, int64, string, error) {
	if len(token) < 32 || len(token) > 512 {
		return "", 0, "", identity.ErrAuthentication
	}
	h := hashToken(token)
	var id string
	var generation int64
	err := s.Store.DB.QueryRowContext(ctx, "SELECT identity_id,generation FROM sf_workload_credentials WHERE token_hash=$1", h).Scan(&id, &generation)
	if err != nil {
		return "", 0, "", identity.ErrAuthentication
	}
	return id, generation, h, nil
}

func (s *Service) Authenticate(ctx context.Context, token, instanceID string) (Principal, error) {
	if s.AuthorityURL != "" {
		return s.remoteWorkload(ctx, token, instanceID, AuthorityWorkloadRequest{Action: "authenticate"})
	}
	id, generation, h, err := s.credential(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	w, err := s.Lookup(ctx, id)
	if err != nil {
		return Principal{}, identity.ErrAuthentication
	}
	if !w.Enabled || w.Generation != generation || instanceID == "" || w.InstanceID != instanceID {
		return Principal{}, identity.ErrAuthentication
	}
	p := Principal{Identity: w, Generation: generation, InstanceID: instanceID, InstanceEpoch: w.InstanceEpoch, credentialHash: h}
	err = s.Store.Write(ctx, func(tx *store.Tx) error { _, e := s.ValidateTx(tx, p, ""); return e })
	return p, err
}

func (s *Service) ActivateInstance(ctx context.Context, token, instanceID string) (Principal, error) {
	if s.AuthorityURL != "" {
		return s.remoteWorkload(ctx, token, instanceID, AuthorityWorkloadRequest{Action: "activate"})
	}
	if len(instanceID) < 8 || len(instanceID) > 128 || strings.ContainsAny(instanceID, " \t\r\n") {
		return Principal{}, identity.ErrAuthentication
	}
	id, generation, h, err := s.credential(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	var p Principal
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		d, e := tx.Get("workload_identity", id)
		if e != nil {
			return identity.ErrAuthentication
		}
		w, e := store.Decode[model.WorkloadIdentity](d)
		if e != nil {
			return e
		}
		var actual string
		var current int64
		if e = tx.QueryRowContext(tx.Ctx, "SELECT generation,token_hash FROM sf_workload_credentials WHERE identity_id=$1", id).Scan(&current, &actual); e != nil {
			return identity.ErrAuthentication
		}
		if !w.Enabled || w.Generation != generation || current != generation || subtle.ConstantTimeCompare([]byte(actual), []byte(h)) != 1 {
			return identity.ErrAuthentication
		}
		if w.InstanceID != instanceID {
			w.InstanceID = instanceID
			w.InstanceEpoch++
			w.Version = d.Version + 1
			w.UpdatedMS = s.Store.Now().UnixMilli()
			if _, e = tx.Put("workload_identity", id, d.Version, w); e != nil {
				return e
			}
		}
		p = Principal{Identity: w, Generation: generation, InstanceID: instanceID, InstanceEpoch: w.InstanceEpoch, credentialHash: h}
		return tx.Audit(model.Actor{UserID: id, Source: "workload"}, "workload.instance.start", w.NodeID, "", map[string]any{"instance_id": instanceID, "instance_epoch": w.InstanceEpoch, "generation": generation})
	})
	return p, err
}

func (s *Service) ValidateTx(tx *store.Tx, p Principal, capability string) (model.WorkloadIdentity, error) {
	if s.AuthorityURL != "" {
		current, err := s.remoteWorkload(tx.Ctx, p.credentialToken, p.InstanceID, AuthorityWorkloadRequest{Action: "validate", Capability: capability, ExpectedIdentityVersion: p.Identity.Version, ExpectedGeneration: p.Generation, ExpectedInstanceEpoch: p.InstanceEpoch})
		if err != nil {
			return model.WorkloadIdentity{}, err
		}
		if store.Hash(current.Identity) != store.Hash(p.Identity) {
			return model.WorkloadIdentity{}, identity.ErrAuthentication
		}
		return current.Identity, nil
	}
	w, err := s.LookupTx(tx, p.Identity.ID)
	if err != nil {
		return w, identity.ErrAuthentication
	}
	if !w.Enabled || w.Version != p.Identity.Version || w.Generation != p.Generation || w.InstanceID != p.InstanceID || w.InstanceEpoch != p.InstanceEpoch || w.NodeID != p.Identity.NodeID || w.Program != p.Identity.Program || w.Purpose != p.Identity.Purpose || p.credentialHash == "" {
		return w, identity.ErrAuthentication
	}
	var generation int64
	var h string
	if err = tx.QueryRowContext(tx.Ctx, "SELECT generation,token_hash FROM sf_workload_credentials WHERE identity_id=$1", w.ID).Scan(&generation, &h); err != nil {
		return w, identity.ErrAuthentication
	}
	if generation != p.Generation || subtle.ConstantTimeCompare([]byte(h), []byte(p.credentialHash)) != 1 {
		return w, identity.ErrAuthentication
	}
	if capability != "" && !slices.Contains(w.Capabilities, capability) {
		return w, identity.ErrDenied
	}
	return w, nil
}

func (s *Service) FromRequest(r *http.Request) (Principal, error) {
	return s.Authenticate(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.Header.Get("X-SF-Instance-ID"))
}

func (p Principal) Actor() model.Actor { return model.Actor{UserID: p.Identity.ID, Source: "workload"} }
func (p Principal) String() string {
	return fmt.Sprintf("%s/%s/%s/%d", p.Identity.ID, p.Identity.NodeID, p.InstanceID, p.Generation)
}
