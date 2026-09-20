// sf-hr-plugin adapts an HTTPS organization feed to the process plugin protocol.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"competition2026/product/platform/internal/plugins"
)

func run() error {
	var request plugins.PullRequest
	if err := json.NewDecoder(io.LimitReader(os.Stdin, 2<<20)).Decode(&request); err != nil {
		return err
	}
	if request.Method != "pull" {
		return fmt.Errorf("unsupported plugin method")
	}
	endpoint, _ := request.Config["endpoint"].(string)
	u, err := url.Parse(endpoint)
	if err != nil {
		return err
	}
	ip := net.ParseIP(u.Hostname())
	local := u.Hostname() == "localhost" || ip != nil && ip.IsLoopback()
	if u.Host == "" || u.User != nil || (u.Scheme != "https" && !(u.Scheme == "http" && local)) {
		return fmt.Errorf("organization feed requires HTTPS")
	}
	query := u.Query()
	query.Set("after", strconv.FormatInt(request.After, 10))
	u.RawQuery = query.Encode()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	if credential, ok := request.Credentials.(map[string]any); ok {
		if token, ok := credential["token"].(string); ok {
			httpRequest.Header.Set("Authorization", "Bearer "+token)
		}
	}
	client := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("organization feed redirect refused") }}
	response, err := client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("organization feed unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return fmt.Errorf("organization feed returned %d", response.StatusCode)
	}
	var result plugins.OrganizationSync
	if err = json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result); err != nil {
		return err
	}
	result.Source = request.Source
	return json.NewEncoder(os.Stdout).Encode(result)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
