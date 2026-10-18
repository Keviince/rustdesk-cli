package config

import "testing"

func TestRelayServerRoundtrip(t *testing.T) {
	writeTempConfig(t, "server = \"id.example.com\"\nrelay_server = \"[2001:db8::1]:443\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.RelayServer != "[2001:db8::1]:443" || got.Server != cfg.Server {
		t.Fatalf("relay/server lost after save: %+v", got)
	}
}
