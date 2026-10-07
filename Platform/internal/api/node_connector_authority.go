package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"competition2026/product/platform/internal/cloudsync"
	"competition2026/product/platform/internal/configcenter"
	"competition2026/product/platform/internal/deviceconfig"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

// Public connector records enter this authority only through the admitted edge
// synchronization path. Endpoint addresses are read from current node registration.
func (s *Server) registerNodeConnectorAuthority(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/authority/connector-metadata", func(w http.ResponseWriter, r *http.Request) {
		if !configurationServiceTransport(r) {
			fail(w, identity.ErrDenied)
			return
		}
		var in struct {
			References []model.ConfigurationReference `json:"references"`
		}
		if e := decode(r, &in); e != nil {
			fail(w, e)
			return
		}
		if in.References == nil || len(in.References) > 512 {
			fail(w, identity.ErrDenied)
			return
		}
		out := make([]model.ConnectorRegistration, 0, len(in.References))
		err := s.Store.Write(r.Context(), func(tx *store.Tx) error {
			for _, ref := range in.References {
				if ref.Kind != "connector" || ref.ID == "" || ref.Version < 0 {
					return identity.ErrDenied
				}
				kind, id := "connector_configuration_version", model.ConnectorSecretReference(ref.ID, ref.Version)
				if ref.Version == 0 {
					kind, id = "connector_configuration", ref.ID
				}
				d, e := tx.Read(kind, id)
				if e != nil {
					return e
				}
				c, e := store.Decode[model.ConnectorConfiguration](d)
				if e != nil {
					return e
				}
				if c.ID != ref.ID || ref.Version > 0 && c.Version != ref.Version || c.ID != c.EdgeID+"/"+c.ConnectorID {
					return store.ErrConflict
				}
				digest := configcenter.ConnectorDigest(c)
				if ref.Digest != "" && ref.Digest != digest {
					return store.ErrConflict
				}
				public, e := deviceconfig.Validate(deviceconfig.Request{Protocol: c.Protocol, Config: c.Config})
				if e != nil {
					return e
				}
				if !public.Valid || public.Parameters.Connection["password"] != nil {
					return identity.ErrDenied
				}
				edgeDoc, e := tx.Read("entity", c.EdgeID)
				if e != nil {
					return e
				}
				edge, e := store.Decode[model.Entity](edgeDoc)
				if e != nil {
					return e
				}
				if edge.Kind != "edge" || edge.Status != "active" {
					return identity.ErrDenied
				}
				var registration cloudsync.Registration
				if e = store.DecodeJSON(edge.Config, &registration); e != nil {
					return e
				}
				endpoint, e := url.Parse(registration.ConfigurationURL)
				if e != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" || len(registration.CertificateSHA256) != 64 || registration.AuditPublicKey == "" || registration.EncryptionPublicKey == "" {
					return errors.New("registered secure connector source required")
				}
				out = append(out, model.ConnectorRegistration{Reference: model.ConfigurationReference{Kind: "connector", ID: c.ID, Version: c.Version, Digest: digest}, Configuration: c, NodeID: c.EdgeID, NodeVersion: edge.Version, SourceURL: strings.TrimRight(endpoint.String(), "/"), CertificateSHA256: registration.CertificateSHA256})
			}
			return nil
		})
		if err != nil {
			fail(w, fmt.Errorf("connector authority: %w", err))
			return
		}
		respond(w, 200, out)
	})
}
