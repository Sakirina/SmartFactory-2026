package api

import (
	"net/http"

	"competition2026/product/platform/internal/application"
	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
)

func configurationServiceTransport(r *http.Request) bool {
	if r.TLS == nil {
		return releaseTransport(r)
	}
	return len(r.TLS.VerifiedChains) > 0 && len(r.TLS.PeerCertificates) > 0 && r.TLS.PeerCertificates[0].Subject.CommonName == "config-service"
}

func (s *Server) registerConfigurationAuthority(mux *http.ServeMux) {
	s.registerNodeConnectorAuthority(mux)
	if s.Mode != "cloud" {
		return
	}
	mux.HandleFunc("POST /internal/authority/workload", func(w http.ResponseWriter, r *http.Request) {
		if !releaseTransport(r) {
			fail(w, identity.ErrDenied)
			return
		}
		var in struct {
			Action                  string `json:"action"`
			Capability              string `json:"capability"`
			ExpectedIdentityVersion int64  `json:"expected_identity_version"`
			ExpectedGeneration      int64  `json:"expected_generation"`
			ExpectedInstanceEpoch   int64  `json:"expected_instance_epoch"`
		}
		if err := decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		service := s.NodeIdentities()
		if in.Action == "activate" {
			p, err := service.ActivateInstance(r.Context(), bearer(r), r.Header.Get("X-SF-Instance-ID"))
			if err != nil {
				fail(w, err)
				return
			}
			respond(w, 200, p.Identity)
			return
		}
		p, err := service.FromRequest(r)
		if err != nil {
			fail(w, err)
			return
		}
		if in.Action != "authenticate" && in.Action != "validate" {
			fail(w, APIError{400, "unknown workload authorization action"})
			return
		}
		if in.ExpectedIdentityVersion != 0 && p.Identity.Version != in.ExpectedIdentityVersion || in.ExpectedGeneration != 0 && p.Generation != in.ExpectedGeneration || in.ExpectedInstanceEpoch != 0 && p.InstanceEpoch != in.ExpectedInstanceEpoch {
			fail(w, identity.ErrAuthentication)
			return
		}
		err = s.Store.Write(r.Context(), func(tx *store.Tx) error {
			var e error
			p.Identity, e = service.ValidateTx(tx, p, in.Capability)
			return e
		})
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, p.Identity)
	})
	mux.HandleFunc("GET /internal/authority/workloads/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !configurationServiceTransport(r) {
			fail(w, identity.ErrDenied)
			return
		}
		v, err := s.NodeIdentities().Lookup(r.Context(), r.PathValue("id"))
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, v)
	})
	mux.HandleFunc("POST /internal/authority/user", func(w http.ResponseWriter, r *http.Request) {
		if !configurationServiceTransport(r) {
			fail(w, identity.ErrDenied)
			return
		}
		p, err := s.Identity.Authenticate(r.Context(), bearer(r))
		if err != nil {
			fail(w, err)
			return
		}
		var in struct {
			Action    string   `json:"action"`
			Resources []string `json:"resources"`
		}
		if err = decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		if err = s.BusinessApplication().AuthorizeConfigurationUser(r.Context(), p, in.Action, in.Resources); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"principal": p, "session_document": p.SessionDocument, "session_version": p.SessionVersion})
	})
	mux.HandleFunc("POST /internal/authority/release-target", func(w http.ResponseWriter, r *http.Request) {
		if !releaseTransport(r) {
			fail(w, identity.ErrDenied)
			return
		}
		p, err := s.NodeIdentities().FromRequest(r)
		if err != nil {
			fail(w, err)
			return
		}
		var in application.ReleaseConfigurationAuthorization
		if err = decode(r, &in); err != nil {
			fail(w, err)
			return
		}
		out, err := s.ReleaseApplication().AuthorizeConfigurationTarget(r.Context(), p, in)
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, out)
	})
}
