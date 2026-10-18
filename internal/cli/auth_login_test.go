package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"rdcli/internal/config"
	pb "rdcli/internal/hbb/proto"
)

func loginTestConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"RDC_API_SERVER", "RDC_API_USERNAME", "RDC_API_PASSWORD", "RDC_TOKEN", "RDC_PASSWORD"} {
		t.Setenv(key, "")
	}
	path := filepath.Join(home, "rdcli", "config.toml")
	t.Setenv("RDC_CONFIG", path)
	cfg := &config.Config{Server: "rv.example.com:21116", RelayServer: "relay.example.com:21117", Key: "server-key", AccessToken: "old-token", APIServer: "https://book.example.com", APIUsername: "book-user", APIPassword: "book-password", Peers: map[string]config.Peer{"123": {Name: "peer", Password: "peer-password"}}, Tags: map[string][]string{"123": {"prod"}}}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAuthLoginTokenReusedForMultipleShellTargets(t *testing.T) {
	loginTestConfig(t)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodPost || r.URL.Path != "/api/login" {
			t.Errorf("unexpected login endpoint: %s %s", r.Method, r.URL.Path)
		}
		w.Write([]byte(`{"type":"access_token","access_token":"saved-login-token","user":{"name":"alice"}}`))
	}))
	defer srv.Close()
	t.Setenv("RDC_API_PASSWORD", "account-password")
	t.Setenv("RDC_PASSWORD", "environment-peer-password")
	if _, _, err := executeAuthTest([]string{"auth", "login", "--api-server", srv.URL, "-u", "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ ref, password string }{{"123", "first-peer-password"}, {"456", "second-peer-password"}} {
		cmd := NewRootCmd()
		if err := cmd.ParseFlags([]string{"-c", tc.ref, "--password", tc.password}); err != nil {
			t.Fatal(err)
		}
		peerID, password, remArgs, err := resolveTarget(cmd, []string{"whoami"})
		if err != nil || peerID != tc.ref || len(remArgs) != 1 || remArgs[0] != "whoami" {
			t.Fatalf("resolve shell target: id=%s args=%v err=%v", peerID, remArgs, err)
		}
		opts := dialOpts(cmd, peerID, password, pb.ConnType_TERMINAL)
		if opts.Token != "saved-login-token" || opts.Password != tc.password || opts.Server != "rv.example.com:21116" || opts.Key != "server-key" {
			t.Fatal("shell connection did not preserve login token, peer password or server settings")
		}
	}
	if requests != 1 {
		t.Fatalf("login called %d times, want once", requests)
	}
}

func executeAuthTest(args []string, input string) (string, string, error) {
	cmd := NewRootCmd()
	cmd.SilenceUsage = true
	cmd.SilenceErrors = true
	cmd.SetArgs(args)
	cmd.SetIn(strings.NewReader(input))
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	return stdout.String(), stderr.String(), err
}

func TestAuthLoginSaveAndReuse(t *testing.T) {
	path := loginTestConfig(t)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	var uuids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload["username"] != "alice" || payload["password"] != " account-password " {
			t.Error("incorrect username/password")
		}
		uuids = append(uuids, payload["uuid"].(string))
		w.Write([]byte(`{"type":"access_token","access_token":"new-secret-token"}`))
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		args := []string{"auth", "login", "--password-stdin", "--json"}
		if i == 0 {
			args = append(args, "--api-server", srv.URL, "-u", "alice")
		}
		out, stderr, err := executeAuthTest(args, " account-password \r\n")
		if err != nil {
			t.Fatal(err)
		}
		if stderr != "" || strings.Contains(out, "new-secret-token") || strings.Contains(out, "account-password") {
			t.Fatal("credentials or diagnostics leaked to output")
		}
		var result struct {
			Version  int  `json:"version"`
			LoggedIn bool `json:"logged_in"`
		}
		if err := json.Unmarshal([]byte(out), &result); err != nil || result.Version != 1 || !result.LoggedIn {
			t.Fatalf("invalid JSON: %s", out)
		}
	}
	if len(uuids) != 2 || uuids[0] == "" || uuids[0] != uuids[1] {
		t.Fatal("device UUID was not reused")
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AccessToken != "new-secret-token" || cfg.LoginServer != srv.URL+"/api/login" || cfg.LoginUsername != "alice" || cfg.LoginUUID != uuids[0] {
		t.Fatalf("login settings were not persisted: %+v", cfg)
	}
	if cfg.APIServer != "https://book.example.com" || cfg.APIUsername != "book-user" || cfg.APIPassword != "book-password" || cfg.Server != "rv.example.com:21116" || cfg.RelayServer != "relay.example.com:21117" || cfg.Peers["123"].Password != "peer-password" || len(cfg.Tags["123"]) != 1 {
		t.Fatal("existing connection/address book settings changed")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("account-password")) {
		t.Fatal("account password saved")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if info.Mode().Perm() != 0600 {
			t.Fatalf("config permissions %o", info.Mode().Perm())
		}
	}
	cmd := NewRootCmd()
	if getTokenFlag(cmd) != "new-secret-token" {
		t.Fatal("saved token not used for peer connections")
	}
	out, _, err := executeAuthTest([]string{"auth", "status", "--json"}, "")
	if err != nil || strings.Contains(out, "new-secret-token") {
		t.Fatalf("status leaked token: %v", err)
	}
	var status authStatusJSON
	if err := json.Unmarshal([]byte(out), &status); err != nil || !status.LoggedIn || status.Source != "config" {
		t.Fatalf("unexpected auth status: %s", out)
	}
	if _, _, err := executeAuthTest([]string{"auth", "logout"}, ""); err != nil {
		t.Fatal(err)
	}
	cfg, err = config.Load()
	if err != nil || cfg.AccessToken != "" {
		t.Fatal("logout did not clear saved token")
	}
}

func TestAuthLoginFailuresDoNotChangeConfig(t *testing.T) {
	for _, body := range []string{`{"error":"incorrect password"}`, `{"access_token":""}`, `{"type":"tfa_check","access_token":"temporary-secret"}`, `<html>unavailable</html>`} {
		t.Run(body, func(t *testing.T) {
			path := loginTestConfig(t)
			before, _ := os.ReadFile(path)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer srv.Close()
			out, _, err := executeAuthTest([]string{"auth", "login", "--api-server", srv.URL, "-u", "alice", "-p", "account-password"}, "")
			if err == nil || out != "" {
				t.Fatal("expected login failure without success output")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed login changed saved config")
			}
		})
	}
}

func TestAuthLoginCredentialPrecedence(t *testing.T) {
	loginTestConfig(t)
	var username, password string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct{ Username, Password string }
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		username, password = payload.Username, payload.Password
		w.Write([]byte(`{"access_token":"new-token"}`))
	}))
	defer srv.Close()
	t.Setenv("RDC_API_SERVER", srv.URL)
	t.Setenv("RDC_API_USERNAME", "env-user")
	t.Setenv("RDC_API_PASSWORD", "env-password")
	t.Setenv("RDC_PASSWORD", "peer-secret-must-not-be-used")
	for _, tc := range []struct {
		args              []string
		input, user, pass string
	}{
		{[]string{"auth", "login"}, "", "env-user", "env-password"},
		{[]string{"auth", "login", "-u", "flag-user", "-p", "flag-password"}, "", "flag-user", "flag-password"},
		{[]string{"auth", "login", "--password-stdin"}, "stdin-password\n", "env-user", "stdin-password"},
	} {
		if _, _, err := executeAuthTest(tc.args, tc.input); err != nil {
			t.Fatal(err)
		}
		if username != tc.user || password != tc.pass {
			t.Fatal("credential precedence mismatch")
		}
	}
	t.Setenv("RDC_API_SERVER", "http://127.0.0.1:1")
	if _, _, err := executeAuthTest([]string{"auth", "login", "--api-server", srv.URL}, ""); err != nil {
		t.Fatalf("API server flag did not override environment: %v", err)
	}
}

func TestAuthLoginUsage(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		input string
	}{
		{[]string{"auth", "login"}, ""},
		{[]string{"auth", "login", "--api-server", "https://api.example.com", "-u", "alice"}, ""},
		{[]string{"auth", "login", "--api-server", "https://api.example.com", "-u", "alice", "--password-stdin"}, ""},
		{[]string{"auth", "login", "--api-server", "https://api.example.com", "-u", "alice", "-p", "password", "--password-stdin"}, "other"},
		{[]string{"auth", "login", "--api-server", "http://api.example.com", "-u", "alice", "-p", "password"}, ""},
		{[]string{"auth", "login", "--api-server", "https://api.example.com", "-u", "alice", "--password-stdin"}, strings.Repeat("p", 65537)},
	} {
		loginTestConfig(t)
		t.Setenv("RDC_PASSWORD", "peer-password")
		out, _, err := executeAuthTest(tc.args, tc.input)
		if _, ok := err.(*UsageError); !ok || out != "" {
			t.Fatalf("expected usage error: %v", err)
		}
	}
}

func TestAuthStatusTokenSources(t *testing.T) {
	loginTestConfig(t)
	t.Setenv("RDC_TOKEN", "env-secret-token")
	for _, tc := range []struct {
		args           []string
		source, secret string
	}{
		{[]string{"auth", "status", "--json"}, "env", "env-secret-token"},
		{[]string{"auth", "status", "--json", "--token", "flag-secret-token"}, "flag", "flag-secret-token"},
	} {
		out, _, err := executeAuthTest(tc.args, "")
		if err != nil || strings.Contains(out, tc.secret) {
			t.Fatalf("status credential leak or error: %v", err)
		}
		var status authStatusJSON
		if err := json.Unmarshal([]byte(out), &status); err != nil || status.Source != tc.source || !status.LoggedIn {
			t.Fatalf("unexpected status: %s", out)
		}
	}
}
