package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"competition2026/product/platform/internal/identity"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) remoteConfigurationReports(ctx context.Context, token string) ([]model.ConfigurationReport, error) {
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(call, "GET", strings.TrimRight(s.ConfigURL, "/")+"/api/sf/v1/configuration-reports", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := s.HTTPClient
	if client == nil {
		return nil, errors.New("configuration report transport unavailable")
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("configuration report service unavailable")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		if res.StatusCode == 401 {
			return nil, identity.ErrAuthentication
		}
		if res.StatusCode == 403 {
			return nil, identity.ErrDenied
		}
		return nil, errors.New("configuration report service rejected request")
	}
	var out []model.ConfigurationReport
	decoder := json.NewDecoder(io.LimitReader(res.Body, 8<<20))
	decoder.UseNumber()
	if err = decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}
