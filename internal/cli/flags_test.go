package cli

import (
	"os"
	"path/filepath"
	"testing"

	"rdcli/internal/hbb"
	pb "rdcli/internal/hbb/proto"
)

func TestConnectionConfigPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("server = \"cfg-id:21116\"\nrelay_server = \"cfg-relay:443\"\nkey = \"cfg-key\"\naccess_token = \"cfg-token\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RDC_CONFIG", path)
	for _, key := range []string{"RDC_SERVER", "RDC_RELAY", "RDC_KEY", "RDC_TOKEN"} {
		t.Setenv(key, "")
	}
	check := func(wantServer, wantRelay, wantKey, wantToken string) {
		t.Helper()
		cmd := NewRootCmd()
		opts := dialOpts(cmd, "peer", "pw", pb.ConnType_TERMINAL)
		if opts.Server != wantServer || opts.RelayServer != wantRelay || opts.Key != wantKey || opts.Token != wantToken || opts.ForceRelay {
			t.Fatalf("unexpected opts: %+v", opts)
		}
	}
	check("cfg-id:21116", "cfg-relay:443", "cfg-key", "cfg-token")
	t.Setenv("RDC_SERVER", "env-id")
	t.Setenv("RDC_RELAY", "env-relay")
	t.Setenv("RDC_KEY", "env-key")
	t.Setenv("RDC_TOKEN", "env-token")
	check("env-id", "env-relay", "env-key", "env-token")
	cmd := NewRootCmd()
	if err := cmd.ParseFlags([]string{"--server=flag-id", "--relay-server=flag-relay", "--key=flag-key", "--token=flag-token"}); err != nil {
		t.Fatal(err)
	}
	opts := dialOpts(cmd, "peer", "pw", pb.ConnType_TERMINAL)
	if opts.Server != "flag-id" || opts.RelayServer != "flag-relay" || opts.Key != "flag-key" || opts.Token != "flag-token" || opts.ForceRelay {
		t.Fatalf("flags did not win: %+v", opts)
	}
}

func TestConnectionDefaults(t *testing.T) {
	t.Setenv("RDC_CONFIG", filepath.Join(t.TempDir(), "missing.toml"))
	for _, key := range []string{"RDC_SERVER", "RDC_RELAY", "RDC_KEY"} {
		t.Setenv(key, "")
	}
	cmd := NewRootCmd()
	if getServerFlag(cmd) != hbb.DefaultRendezvousServer || getRelayServerFlag(cmd) != "" || getKeyFlag(cmd) != "" {
		t.Fatal("unexpected connection defaults")
	}
}
