package hbb

import (
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
)

func normalizeEndpoint(address string, defaultPort int) (string, error) {
	address = strings.TrimSpace(address)
	if address == "" || strings.ContainsAny(address, "/\\?#@<>\"' \t\r\n") {
		return "", fmt.Errorf("invalid server address %q: expected host[:port]", address)
	}
	host, port := address, strconv.Itoa(defaultPort)
	if strings.HasPrefix(address, "[") && strings.HasSuffix(address, "]") {
		host = address[1 : len(address)-1]
		if ip, err := netip.ParseAddr(host); err != nil || !ip.Is6() {
			return "", fmt.Errorf("invalid ipv6 address %q", address)
		}
	} else if _, err := netip.ParseAddr(address); err != nil && strings.ContainsAny(address, ":[]") {
		var splitErr error
		host, port, splitErr = net.SplitHostPort(address)
		if splitErr != nil {
			return "", fmt.Errorf("invalid server address %q: %w", address, splitErr)
		}
	}
	if host == "" {
		return "", fmt.Errorf("invalid server address %q: host is empty", address)
	}
	if _, err := netip.ParseAddr(host); err != nil {
		for _, ch := range host {
			if !unicode.IsLetter(ch) && !unicode.IsDigit(ch) && ch != '.' && ch != '-' && ch != '_' {
				return "", fmt.Errorf("invalid server host %q", host)
			}
		}
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", fmt.Errorf("invalid server port %q", port)
	}
	return net.JoinHostPort(host, strconv.FormatUint(n, 10)), nil
}

func normalizeRendezvous(server string) (string, error) {
	if strings.TrimSpace(server) == "" {
		server = DefaultRendezvousServer
	}
	return normalizeEndpoint(server, 21116)
}

func normalizeRelay(server string) (string, error) {
	return normalizeEndpoint(server, relayPort)
}

func selectRelay(override, announced, rendezvous string) (string, error) {
	if override != "" {
		return normalizeRelay(override)
	}
	if announced != "" {
		return normalizeRelay(announced)
	}
	addr, err := normalizeRendezvous(rendezvous)
	if err != nil {
		return "", err
	}
	host, port, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(port)
	if n == 65535 {
		return "", fmt.Errorf("cannot derive relay port from %s: set --relay-server or RDC_RELAY", addr)
	}
	return net.JoinHostPort(host, strconv.Itoa(n+1)), nil
}
