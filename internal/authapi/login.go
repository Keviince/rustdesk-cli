package authapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"
)

const maxResponseSize = 1 << 20

type LoginOptions struct {
	Server, Username, Password, UUID string
	AllowHTTP                        bool
	Timeout                          time.Duration
}

func Endpoint(server string, allowHTTP bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(server))
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("invalid API server: use an http(s) URL without credentials, query or fragment")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("invalid API server: use an http(s) URL")
	}
	if u.Scheme == "http" && !allowHTTP {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return "", errors.New("api password login requires HTTPS; use --allow-http only for a trusted HTTP deployment")
		}
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	switch {
	case strings.HasSuffix(u.Path, "/api/login"):
	case strings.HasSuffix(u.Path, "/api"):
		u.Path += "/login"
	default:
		u.Path += "/api/login"
	}
	return u.String(), nil
}

func Login(ctx context.Context, opts LoginOptions) (string, error) {
	endpoint, err := Endpoint(opts.Server, opts.AllowHTTP)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(opts.Username) == "" || opts.Password == "" || opts.UUID == "" {
		return "", errors.New("api login requires a username, password and device UUID")
	}
	// Matches lejianwen/rustdesk-api http/request/api/user.go LoginForm.
	body, err := json.Marshal(map[string]any{
		"username": opts.Username, "password": opts.Password, "type": "account",
		"id": "rdcli-" + opts.UUID, "uuid": opts.UUID, "autoLogin": true,
		"deviceInfo": map[string]string{"os": runtime.GOOS, "type": "client", "name": "rdcli"},
	})
	if err != nil {
		return "", errors.New("encode API login request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("build API login request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Language", "en")
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("api login request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return "", errors.New("api login refused a redirect; specify the final API server URL")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("api login failed (HTTP %d): check account credentials and whether password login is enabled", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return "", errors.New("read API login response")
	}
	if len(data) > maxResponseSize {
		return "", errors.New("api login response is too large")
	}
	var result struct {
		Type        string `json:"type"`
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.New("api login returned invalid JSON")
	}
	if result.Error != "" {
		return "", errors.New("api login rejected credentials or password login is disabled")
	}
	switch result.Type {
	case "", "access_token":
	case "tfa_check", "email_check":
		return "", errors.New("api login requires additional verification; complete login in RustDesk and use auth import-gui or auth set")
	default:
		return "", errors.New("api login returned an unsupported authentication response")
	}
	token := strings.TrimSpace(result.AccessToken)
	if token == "" {
		return "", errors.New("api login response missing access_token")
	}
	return token, nil
}
