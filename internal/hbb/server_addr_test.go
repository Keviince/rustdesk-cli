package hbb

import "testing"

func TestNormalizeRelay(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"relay.example.com", "relay.example.com:21117"},
		{" relay.example.com:443 ", "relay.example.com:443"},
		{"192.0.2.1", "192.0.2.1:21117"},
		{"2001:db8::1", "[2001:db8::1]:21117"},
		{"[2001:db8::1]", "[2001:db8::1]:21117"},
		{"[2001:db8::1]:443", "[2001:db8::1]:443"},
		{"[fe80::1%eth0]:21117", "[fe80::1%eth0]:21117"},
	} {
		got, err := normalizeRelay(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("normalizeRelay(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", " ", ":21117", "<relay_server[:port]>", "https://relay.example.com", "host:", "host:0", "host:65536", "host:nope", "[::1", "[host]", "bad host", "foo:bar:baz", "host/path"} {
		if got, err := normalizeRelay(bad); err == nil {
			t.Errorf("accepted invalid address %q as %q", bad, got)
		}
	}
}

func TestSelectRelay(t *testing.T) {
	for _, tc := range []struct{ override, announced, server, want string }{
		{"override:443", "announced:21117", "id:21116", "override:443"},
		{"override", ":21117", "id", "override:21117"},
		{"", "announced", "id", "announced:21117"},
		{"", "", "id", "id:21117"},
		{"", "", "id:30000", "id:30001"},
		{"", "", "[2001:db8::1]:30000", "[2001:db8::1]:30001"},
		{"", "", "", "rs-ny.rustdesk.com:21117"},
	} {
		got, err := selectRelay(tc.override, tc.announced, tc.server)
		if err != nil || got != tc.want {
			t.Errorf("selectRelay(%q,%q,%q) = %q,%v; want %q", tc.override, tc.announced, tc.server, got, err, tc.want)
		}
	}
	for _, tc := range []struct{ override, announced, server string }{
		{":21117", "valid", "id"}, {"", ":21117", "id"}, {"", "", ":21116"}, {"", "", "id:65535"},
	} {
		if _, err := selectRelay(tc.override, tc.announced, tc.server); err == nil {
			t.Errorf("expected invalid relay selection: %+v", tc)
		}
	}
}
