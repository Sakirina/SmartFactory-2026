package nodeidentity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

type AuthorityWorkloadRequest struct {
	Action                  string `json:"action"`
	Capability              string `json:"capability,omitempty"`
	ExpectedIdentityVersion int64  `json:"expected_identity_version,omitempty"`
	ExpectedGeneration      int64  `json:"expected_generation,omitempty"`
	ExpectedInstanceEpoch   int64  `json:"expected_instance_epoch,omitempty"`
}

var ErrAuthorityUnavailable = errors.New("cloud authority unavailable")

type AuthorityUserRequest struct {
	Action    string   `json:"action"`
	Resources []string `json:"resources"`
}

type AuthorityUserResponse struct {
	Principal       identity.Principal `json:"principal"`
	SessionDocument string             `json:"session_document"`
	SessionVersion  int64              `json:"session_version"`
}

func (s *Service) AuthorityCall(ctx context.Context, method, path, token, instance string, body, out any) error {
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(s.AuthorityURL, "/")+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-SF-Instance-ID", instance)
	req.Header.Set("Content-Type", "application/json")
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	res, err := client.Do(req.WithContext(call))
	if err != nil {
		return ErrAuthorityUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		switch res.StatusCode {
		case 401:
			return identity.ErrAuthentication
		case 403:
			return identity.ErrDenied
		case 409:
			return fmt.Errorf("%w: authority revision conflict", store.ErrConflict)
		case 404:
			return store.ErrNotFound
		default:
			return ErrAuthorityUnavailable
		}
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 4<<20))
	decoder.UseNumber()
	if err = decoder.Decode(out); err != nil {
		return errors.New("invalid authority response")
	}
	return nil
}

func (s *Service) remoteWorkload(ctx context.Context, token, instance string, in AuthorityWorkloadRequest) (Principal, error) {
	if token == "" || instance == "" {
		return Principal{}, identity.ErrAuthentication
	}
	var w model.WorkloadIdentity
	if err := s.AuthorityCall(ctx, "POST", "/internal/authority/workload", token, instance, in, &w); err != nil {
		return Principal{}, err
	}
	if !w.Enabled || w.InstanceID != instance {
		return Principal{}, identity.ErrAuthentication
	}
	return Principal{Identity: w, Generation: w.Generation, InstanceID: instance, InstanceEpoch: w.InstanceEpoch, credentialToken: token}, nil
}

func (s *Service) remoteLookup(ctx context.Context, id string) (model.WorkloadIdentity, error) {
	var w model.WorkloadIdentity
	err := s.AuthorityCall(ctx, "GET", "/internal/authority/workloads/"+url.PathEscape(id), "", "", nil, &w)
	return w, err
}

func (s *Service) AuthorizeUser(ctx context.Context, token, action string, resources []string) (identity.Principal, error) {
	var out AuthorityUserResponse
	err := s.AuthorityCall(ctx, "POST", "/internal/authority/user", token, "", AuthorityUserRequest{Action: action, Resources: resources}, &out)
	out.Principal.SessionDocument = out.SessionDocument
	out.Principal.SessionVersion = out.SessionVersion
	return out.Principal, err
}

func (p Principal) CredentialToken() string { return p.credentialToken }
