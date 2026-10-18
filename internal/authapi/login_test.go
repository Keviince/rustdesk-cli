package authapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEndpoint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://rd.example.com", "https://rd.example.com/api/login"},
		{"https://rd.example.com/api/", "https://rd.example.com/api/login"},
		{"https://rd.example.com/proxy/api/login/", "https://rd.example.com/proxy/api/login"},
		{"http://127.0.0.1:21114", "http://127.0.0.1:21114/api/login"},
		{"http://LOCALHOST:21114", "http://LOCALHOST:21114/api/login"},
		{"http://[::1]:21114", "http://[::1]:21114/api/login"},
	} {
		got, err := Endpoint(tc.in, false)
		if err != nil || got != tc.want {
			t.Errorf("Endpoint(%q)=%q,%v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "rd.example.com", "ftp://rd.example.com", "https://user:secret@rd.example.com", "https://rd.example.com?password=secret", "https://rd.example.com#token", "http://rd.example.com:21114", "http://localhost.example.com"} {
		if _, err := Endpoint(bad, false); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Endpoint("http://rd.example.com:21114", true); err != nil {
		t.Fatal(err)
	}
}

func TestLoginPayload(t *testing.T) {
	password := " p\"\\雪 "
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/login" || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "" {
			t.Errorf("incorrect login request: %s %s", r.Method, r.URL.Path)
		}
		var payload struct {
			Username, Password, Type, ID, UUID string
			AutoLogin                          bool
			DeviceInfo                         struct{ OS, Type, Name string }
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Username != "alice" || payload.Password != password || payload.Type != "account" || payload.ID != "rdcli-device-uuid" || payload.UUID != "device-uuid" || !payload.AutoLogin || payload.DeviceInfo.OS != runtime.GOOS || payload.DeviceInfo.Type != "client" || payload.DeviceInfo.Name != "rdcli" {
			t.Error("account login payload mismatch")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"type":"access_token","access_token":"new-token"}`))
	}))
	defer srv.Close()
	token, err := Login(context.Background(), LoginOptions{Server: srv.URL + "/api", Username: "alice", Password: password, UUID: "device-uuid"})
	if err != nil || token != "new-token" {
		t.Fatalf("token=%q err=%v", token, err)
	}
}

func TestLoginResponses(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
	}{
		{"legacy response", `{"access_token":"valid-token"}`, "", 200},
		{"bad credentials", `{"error":"password secret-token"}`, "HTTP 400", 400},
		{"server error", `secret-token`, "HTTP 500", 500},
		{"invalid JSON", `<html>secret-token</html>`, "invalid JSON", 200},
		{"missing token", `{"type":"access_token"}`, "missing access_token", 200},
		{"blank token", `{"access_token":"  "}`, "missing access_token", 200},
		{"application rejection", `{"error":"secret-token","access_token":"secret-token"}`, "rejected", 200},
		{"two factor", `{"type":"tfa_check","access_token":"secret-token"}`, "additional verification", 200},
		{"email verification", `{"type":"email_check","access_token":"secret-token"}`, "additional verification", 200},
		{"unknown challenge", `{"type":"secret-token","access_token":"secret-token"}`, "unsupported", 200},
		{"oversized", strings.Repeat("x", maxResponseSize+1), "too large", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer srv.Close()
			token, err := Login(context.Background(), LoginOptions{Server: srv.URL, Username: "alice", Password: "password", UUID: "uuid"})
			if tc.want == "" {
				if err != nil || token != "valid-token" {
					t.Fatalf("login failed: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) || token != "" {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "secret-token") {
				t.Fatal("server response leaked into error")
			}
		})
	}
}

func TestLoginDoesNotFollowRedirects(t *testing.T) {
	var hits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	_, err := Login(context.Background(), LoginOptions{Server: srv.URL, Username: "alice", Password: "password", UUID: "uuid"})
	if err == nil || !strings.Contains(err.Error(), "redirect") || hits.Load() != 0 {
		t.Fatalf("credential redirect was not rejected: %v", err)
	}
}

func TestLoginTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(100 * time.Millisecond):
		}
	}))
	defer srv.Close()
	_, err := Login(context.Background(), LoginOptions{Server: srv.URL, Username: "alice", Password: "password", UUID: "uuid", Timeout: 10 * time.Millisecond})
	if err == nil {
		t.Fatal("expected timeout")
	}
}

func TestLoginTLSVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted TLS request reached login handler") }))
	defer srv.Close()
	_, err := Login(context.Background(), LoginOptions{Server: srv.URL, Username: "alice", Password: "password", UUID: "uuid"})
	if err == nil {
		t.Fatal("expected certificate validation failure")
	}
}
