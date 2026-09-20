// Package thingsboard adapts stable SmartFactory identifiers to the pinned CE/Edge REST APIs.
package thingsboard

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
	"sync"
	"time"

	"competition2026/product/platform/internal/store"
)

type Client struct {
	URL, Username, Password string
	HTTP                    *http.Client
	mu                      sync.Mutex
	token                   string
}
type HTTPError struct {
	Status  int
	Message string
}

func (e HTTPError) Error() string { return fmt.Sprintf("ThingsBoard HTTP %d: %s", e.Status, e.Message) }

func (c *Client) login(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && !force {
		return c.token, nil
	}
	var result struct {
		Token string `json:"token"`
	}
	if err := c.request(ctx, "POST", "/api/auth/login", "", map[string]string{"username": c.Username, "password": c.Password}, &result); err != nil {
		return "", err
	}
	if result.Token == "" {
		return "", errors.New("ThingsBoard login returned no token")
	}
	c.token = result.Token
	return c.token, nil
}
func (c *Client) Do(ctx context.Context, method, path string, value, result any) error {
	token, err := c.login(ctx, false)
	if err != nil {
		return err
	}
	err = c.request(ctx, method, path, token, value, result)
	var h HTTPError
	if errors.As(err, &h) && h.Status == http.StatusUnauthorized {
		token, err = c.login(ctx, true)
		if err == nil {
			err = c.request(ctx, method, path, token, value, result)
		}
	}
	return err
}
func (c *Client) request(ctx context.Context, method, path, token string, value, result any) error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || !strings.HasPrefix(path, "/api/") {
		return errors.New("invalid ThingsBoard URL")
	}
	var b []byte
	if value != nil {
		b, err = json.Marshal(value)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.URL, "/")+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Authorization", "Bearer "+token)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("ThingsBoard redirect refused") }}
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	b, err = io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		var payload struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(b, &payload)
		if payload.Message == "" {
			payload.Message = http.StatusText(res.StatusCode)
		}
		return HTTPError{res.StatusCode, payload.Message}
	}
	if result != nil && len(b) > 0 {
		return store.DecodeJSON(b, result)
	}
	return nil
}
