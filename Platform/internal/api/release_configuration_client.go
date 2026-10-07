package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"competition2026/product/platform/internal/store"
	"competition2026/product/platform/pkg/model"
)

func (s *Server) releaseConfigurationMetadata(ctx context.Context, refs []model.ConfigurationReference) ([]model.ConfigurationMetadata, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]any{"references": refs})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.ConfigURL, "/")+"/internal/config/v2/metadata", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, APIError{503, "configuration metadata authority is unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, APIError{response.StatusCode, "configuration reference is unavailable from the authority"}
	}
	var out []model.ConfigurationMetadata
	if err = store.DecodeJSONReader(io.LimitReader(response.Body, 2<<20), &out); err != nil {
		return nil, err
	}
	if len(out) != len(refs) {
		return nil, fmt.Errorf("configuration authority returned an incomplete reference set")
	}
	for i, ref := range refs {
		if out[i].Reference != ref {
			return nil, fmt.Errorf("configuration authority returned a different immutable reference")
		}
	}
	return out, nil
}
