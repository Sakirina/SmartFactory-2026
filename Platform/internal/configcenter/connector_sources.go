package configcenter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/nodeidentity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

var ErrConnectorSourceUnavailable = errors.New("owning connector source temporarily unavailable")

func connectorSourceTransportFailure(err error) error {
	var certificateError *tls.CertificateVerificationError
	if errors.Is(err, identity.ErrDenied) || errors.As(err, &certificateError) {
		return identity.ErrDenied
	}
	var requestError *url.Error
	if errors.As(err, &requestError) {
		err = requestError.Err
	}
	var networkError net.Error
	if errors.As(err, &networkError) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrConnectorSourceUnavailable
	}
	return errors.New("owning connector source request rejected")
}

// EnsureConnectorVersions registers public records admitted and synchronized by
// the cloud. Secret bytes and source addresses never come from a request body.
func (s *Service) EnsureConnectorVersions(ctx context.Context, references []model.ConfigurationReference) error {
	service := s.workloadService()
	if service.AuthorityURL == "" || s.OwnedNodeID != "" {
		return nil
	}
	refs := []model.ConfigurationReference{}
	for _, ref := range references {
		if ref.Kind == "connector" {
			refs = append(refs, ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	var proofs []model.ConnectorRegistration
	if err := service.AuthorityCall(ctx, "POST", "/internal/authority/connector-metadata", "", "", map[string]any{"references": refs}, &proofs); err != nil {
		return err
	}
	if len(proofs) != len(refs) {
		return store.ErrConflict
	}
	ids := map[string]bool{}
	for i, proof := range proofs {
		ref := refs[i]
		c := proof.Configuration
		if proof.Reference.Kind != "connector" || proof.Reference.ID != ref.ID || ref.Version > 0 && proof.Reference.Version != ref.Version || ref.Digest != "" && proof.Reference.Digest != ref.Digest || proof.Reference.Digest != ConnectorDigest(c) || c.ID != ref.ID || c.EdgeID != proof.NodeID || c.Version != proof.Reference.Version || proof.NodeVersion < 1 || len(proof.CertificateSHA256) != 64 || !strings.HasPrefix(proof.SourceURL, "https://") {
			return store.ErrConflict
		}
		ids[c.ID] = true
	}
	ordered := make([]string, 0, len(ids))
	for id := range ids {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	return s.Store.Write(ctx, func(tx *store.Tx) error {
		// Latest-pointer snapshots share this collection lock before reading
		// fixed versions. Reserve it first for registration and insertion, then
		// reserve connector identities in a stable order across every reference.
		if err := tx.LockCollection("connector_configuration"); err != nil {
			return err
		}
		for _, id := range ordered {
			if _, err := tx.Get("connector_configuration", id); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		for _, proof := range proofs {
			c := proof.Configuration
			key := model.ConnectorSecretReference(c.ID, c.Version)
			// Existing fixed records are immutable dependencies. Keep their
			// shared guards so reverse fixed-version readers can finish together.
			// Missing records take Get before insertion, without upgrading Read.
			var exists bool
			if err := tx.QueryRowContext(tx.Ctx, "SELECT EXISTS(SELECT 1 FROM documents WHERE kind=$1 AND id=$2)", "connector_configuration_version", key).Scan(&exists); err != nil {
				return err
			}
			var old store.Document
			var e error
			if exists {
				old, e = tx.Read("connector_configuration_version", key)
				if errors.Is(e, store.ErrNotFound) {
					return store.ErrConflict
				}
			} else {
				old, e = tx.Get("connector_configuration_version", key)
			}
			if e == nil {
				prior, e := store.Decode[model.ConnectorConfiguration](old)
				if e != nil {
					return e
				}
				if ConnectorDigest(prior) != proof.Reference.Digest {
					return store.ErrConflict
				}
			} else if errors.Is(e, store.ErrNotFound) {
				if _, e = tx.Put("connector_configuration_version", key, 0, c); e != nil {
					return e
				}
			} else {
				return e
			}
			if old, e := tx.Get("connector_configuration", c.ID); e == nil {
				prior, e := store.Decode[model.ConnectorConfiguration](old)
				if e != nil {
					return e
				}
				if prior.EdgeID != c.EdgeID {
					return identity.ErrDenied
				}
				if prior.Version < c.Version {
					if _, e = tx.Put("connector_configuration", c.ID, old.Version, c); e != nil {
						return e
					}
				}
			} else if errors.Is(e, store.ErrNotFound) {
				if _, e = tx.Put("connector_configuration", c.ID, 0, c); e != nil {
					return e
				}
			} else {
				return e
			}
			if old, e := tx.Get("configuration_connector_source", key); e == nil {
				var previous model.ConnectorRegistration
				if e = store.DecodeJSON(old.Data, &previous); e != nil {
					return e
				}
				if previous.NodeVersion > proof.NodeVersion {
					return store.ErrConflict
				}
			} else if !errors.Is(e, store.ErrNotFound) {
				return e
			}
			if e := tx.SetEphemeral("configuration_connector_source", key, proof); e != nil {
				return e
			}
		}
		return nil
	})
}

func (s *Service) ensureWorkloadConnectors(ctx context.Context, p nodeidentity.Principal) error {
	if s.workloadService().AuthorityURL == "" || s.OwnedNodeID != "" {
		return nil
	}
	refs := []model.ConfigurationReference{}
	if d, e := s.Store.Get(ctx, "configuration_release_target", p.Identity.ID); e == nil {
		var target struct {
			References []model.ConfigurationReference `json:"references"`
		}
		if e = store.DecodeJSON(d.Data, &target); e != nil {
			return e
		}
		refs = target.References
	} else if errors.Is(e, store.ErrNotFound) {
		for _, id := range p.Identity.ConnectorIDs {
			refs = append(refs, model.ConfigurationReference{Kind: "connector", ID: id})
		}
	} else {
		return e
	}
	return s.EnsureConnectorVersions(ctx, refs)
}

func (s *Service) requireCredentialTargetTx(tx *store.Tx, p nodeidentity.Principal, ref model.ConfigurationReference) error {
	d, e := tx.Read("configuration_target", targetID(p.Identity, ref))
	if e != nil {
		return identity.ErrDenied
	}
	var target struct {
		Reference     model.ConfigurationReference `json:"reference"`
		Generation    int64                        `json:"generation"`
		InstanceEpoch int64                        `json:"instance_epoch"`
	}
	if e = store.DecodeJSON(d.Data, &target); e != nil {
		return e
	}
	if target.Reference != ref || target.Generation != p.Generation || target.InstanceEpoch != p.InstanceEpoch {
		return store.ErrConflict
	}
	return nil
}

func (s *Service) resolveRemoteConnector(ctx context.Context, p nodeidentity.Principal, input model.CredentialResolution) (model.CredentialPayload, error) {
	if err := s.EnsureConnectorVersions(ctx, []model.ConfigurationReference{input.Reference}); err != nil {
		return model.CredentialPayload{}, err
	}
	var source model.ConnectorRegistration
	forwarded := model.ConnectorCredentialRequest{Resolution: input}
	var envelope model.ConfigurationEnvelope
	err := s.Store.Write(ctx, func(tx *store.Tx) error {
		w, e := s.workloadService().ValidateTx(tx, p, "credential.resolve")
		if e != nil {
			return e
		}
		if input.Purpose != w.Purpose {
			return identity.ErrDenied
		}
		if e = s.requireCredentialTargetTx(tx, p, input.Reference); e != nil {
			return e
		}
		envelope, e = s.envelopeTx(tx, w, input.Reference)
		if e != nil {
			return e
		}
		if envelope.CredentialRef == "" || envelope.CredentialRef != input.CredentialRef {
			return identity.ErrDenied
		}
		d, e := tx.Read("configuration_connector_source", model.ConnectorSecretReference(input.Reference.ID, input.Reference.Version))
		if e != nil {
			return e
		}
		if e = store.DecodeJSON(d.Data, &source); e != nil {
			return e
		}
		if source.Reference != input.Reference || source.NodeID != w.NodeID {
			return identity.ErrDenied
		}
		if d, e = tx.Read("configuration_release_assignment", w.ID); e == nil {
			var target model.ReleaseConfigurationTarget
			if e = store.DecodeJSON(d.Data, &target); e != nil {
				return e
			}
			forwarded.ReleaseTarget = &target
		} else if !errors.Is(e, store.ErrNotFound) {
			return e
		}
		if w.Purpose == "release-runtime" && forwarded.ReleaseTarget == nil {
			return identity.ErrDenied
		}
		return nil
	})
	if err != nil {
		return model.CredentialPayload{}, err
	}
	raw, err := json.Marshal(forwarded)
	if err != nil {
		return model.CredentialPayload{}, err
	}
	call, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(call, "POST", source.SourceURL+"/internal/config/v2/connector-credentials/resolve", bytes.NewReader(raw))
	if err != nil {
		return model.CredentialPayload{}, err
	}
	req.Header.Set("Authorization", "Bearer "+p.CredentialToken())
	req.Header.Set("X-SF-Instance-ID", p.InstanceID)
	req.Header.Set("Content-Type", "application/json")
	base := s.workloadService().HTTPClient
	if base == nil {
		return model.CredentialPayload{}, errors.New("connector source transport unavailable")
	}
	transport, ok := base.Transport.(*http.Transport)
	if !ok {
		return model.CredentialPayload{}, identity.ErrDenied
	}
	pinned := transport.Clone()
	if pinned.TLSClientConfig == nil {
		return model.CredentialPayload{}, identity.ErrDenied
	}
	pinned.TLSClientConfig = pinned.TLSClientConfig.Clone()
	check := pinned.TLSClientConfig.VerifyConnection
	pinned.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
		if check != nil {
			if e := check(state); e != nil {
				return e
			}
		}
		if len(state.PeerCertificates) == 0 {
			return identity.ErrDenied
		}
		digest := sha256.Sum256(state.PeerCertificates[0].Raw)
		if hex.EncodeToString(digest[:]) != source.CertificateSHA256 {
			return identity.ErrDenied
		}
		return nil
	}
	client := &http.Client{Transport: pinned, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer pinned.CloseIdleConnections()
	res, err := client.Do(req)
	if err != nil {
		return model.CredentialPayload{}, connectorSourceTransportFailure(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		if res.StatusCode == 401 {
			return model.CredentialPayload{}, identity.ErrAuthentication
		}
		if res.StatusCode == 403 {
			return model.CredentialPayload{}, identity.ErrDenied
		}
		if res.StatusCode == 409 {
			return model.CredentialPayload{}, store.ErrConflict
		}
		if res.StatusCode == 404 {
			return model.CredentialPayload{}, store.ErrNotFound
		}
		if res.StatusCode == 408 || res.StatusCode == 429 || res.StatusCode >= 500 {
			return model.CredentialPayload{}, ErrConnectorSourceUnavailable
		}
		return model.CredentialPayload{}, errors.New("owning connector source rejected resolution")
	}
	var out model.CredentialPayload
	if err = store.DecodeJSONReader(io.LimitReader(res.Body, 4<<20), &out); err != nil {
		return out, errors.New("invalid connector source payload")
	}
	if out.Reference != input.Reference || out.CredentialRef != input.CredentialRef {
		return model.CredentialPayload{}, store.ErrConflict
	}
	if err = validateConnectorPayload(envelope, out.Payload); err != nil {
		return model.CredentialPayload{}, err
	}
	err = s.Store.Write(ctx, func(tx *store.Tx) error {
		if _, e := s.workloadService().ValidateTx(tx, p, "credential.resolve"); e != nil {
			return e
		}
		if e := s.requireCredentialTargetTx(tx, p, input.Reference); e != nil {
			return e
		}
		return tx.Audit(p.Actor(), "workload.credential.resolve", p.Identity.NodeID, "", map[string]any{"reference": input.Reference, "credential_ref": input.CredentialRef, "purpose": input.Purpose, "source_node": source.NodeID, "source_registration_version": source.NodeVersion})
	})
	if err != nil {
		return model.CredentialPayload{}, err
	}
	return out, nil
}

func validateConnectorPayload(envelope model.ConfigurationEnvelope, raw json.RawMessage) error {
	if envelope.Connector == nil {
		return identity.ErrDenied
	}
	v, e := deviceconfig.Validate(deviceconfig.Request{Protocol: envelope.Connector.Protocol, Config: raw})
	if e != nil {
		return e
	}
	if !v.Valid {
		return errors.New("connector payload rejected")
	}
	public, e := deviceconfig.PublicConfiguration(v)
	if e != nil {
		return e
	}
	var actual, expected any
	if e = store.DecodeJSON(public.Config, &actual); e != nil {
		return e
	}
	if e = store.DecodeJSON(envelope.Connector.Config, &expected); e != nil {
		return e
	}
	if store.Hash(actual) != store.Hash(expected) {
		return store.ErrConflict
	}
	return nil
}

// RegisterConnectorSource exposes only owned fixed secrets to the verified
// configuration service; cloud revalidates the original workload at this point.
func (s *Service) RegisterConnectorSource(mux *http.ServeMux) {
	if s.OwnedNodeID == "" {
		return
	}
	mux.HandleFunc("POST /internal/config/v2/connector-credentials/resolve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 || r.TLS.PeerCertificates[0].Subject.CommonName != "config-service" {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		var in model.ConnectorCredentialRequest
		if e := configDecode(w, r, &in); e != nil {
			configHTTPError(w, e)
			return
		}
		if in.Resolution.Reference.Kind != "connector" || len(in.Resolution.Reference.Digest) != 64 {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		p, e := s.workloadService().FromRequest(r)
		if e != nil {
			configHTTPError(w, e)
			return
		}
		if p.Identity.NodeID != s.OwnedNodeID || in.Resolution.Purpose != p.Identity.Purpose {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		if in.ReleaseTarget != nil {
			var proof model.ReleaseConfigurationTargetProof
			e = s.workloadService().AuthorityCall(r.Context(), "POST", "/internal/authority/release-target", p.CredentialToken(), p.InstanceID, *in.ReleaseTarget, &proof)
			if e != nil {
				configHTTPError(w, e)
				return
			}
			if store.Hash(proof.Identity) != store.Hash(p.Identity) || proof.DeploymentID != in.ReleaseTarget.DeploymentID || proof.Generation != in.ReleaseTarget.Generation || !sameReferences(proof.References, in.ReleaseTarget.References) {
				configHTTPError(w, identity.ErrDenied)
				return
			}
			found := false
			for _, ref := range proof.References {
				if ref == in.Resolution.Reference {
					found = true
				}
			}
			if !found {
				configHTTPError(w, identity.ErrDenied)
				return
			}
		} else if p.Identity.Purpose == "release-runtime" {
			configHTTPError(w, identity.ErrDenied)
			return
		}
		out, e := s.ResolveCredential(r.Context(), p, in.Resolution)
		if e != nil {
			configHTTPError(w, e)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	})
}
