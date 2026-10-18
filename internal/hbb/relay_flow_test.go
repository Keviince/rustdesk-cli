package hbb

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	pb "rdcli/internal/hbb/proto"
)

func relayFlowListeners(t *testing.T) (net.Listener, net.Listener) {
	t.Helper()
	for i := 0; i < 20; i++ {
		rv, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := rv.Addr().(*net.TCPAddr).Port
		if port < 65535 {
			relay, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port+1)))
			if err == nil {
				t.Cleanup(func() { rv.Close(); relay.Close() })
				return rv, relay
			}
		}
		rv.Close()
	}
	t.Fatal("cannot allocate adjacent rendezvous and relay ports")
	return nil, nil
}

func mockAuthenticatedRendezvous(c net.Conn, privateKey ed25519.PrivateKey) (*SecureConn, error) {
	sc := NewSecureConn(c)
	kp, err := GenerateKeyPair()
	if err != nil {
		return nil, err
	}
	signed := append(ed25519.Sign(privateKey, kp.Pub), kp.Pub...)
	err = writeRendezvousSecure(sc, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_KeyExchange{
		KeyExchange: &pb.KeyExchange{Keys: [][]byte{signed}},
	}})
	if err != nil {
		return nil, err
	}
	msg, err := readRendezvousSecure(sc)
	if err != nil {
		return nil, err
	}
	keys := msg.GetKeyExchange().GetKeys()
	if len(keys) != 2 {
		return nil, fmt.Errorf("invalid client key exchange")
	}
	key, err := DecryptSymmetricKey(keys[1], keys[0], kp.Priv)
	if err != nil {
		return nil, err
	}
	sc.SetKey(key)
	return sc, nil
}

func relayFlowPipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	copyStream := func(dst, src net.Conn) { io.Copy(dst, src); done <- struct{}{} }
	go copyStream(a, b)
	go copyStream(b, a)
	<-done
	a.Close()
	b.Close()
	<-done
}

func TestTerminalRelayFlow(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		force, derive, override, assigned, echo, token bool
	}{
		{name: "empty acknowledgement", force: true},
		{name: "echoed acknowledgement", force: true, echo: true},
		{name: "derived address", force: true, derive: true},
		{name: "direct failure"},
		{name: "assigned relay", force: true, assigned: true},
		{name: "override assigned relay", force: true, assigned: true, override: true},
		{name: "authenticated rendezvous", force: true, token: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverPub, serverPriv, _ := ed25519.GenerateKey(rand.Reader)
			peerPub, peerPriv, _ := ed25519.GenerateKey(rand.Reader)
			rv, relay := relayFlowListeners(t)
			peerAddr, peerDone := runMockTerminalPeer(t, peerPriv, "relay-peer", "pw", "1.4.1")
			relayAddr := relay.Addr().String()
			paired := make(chan struct{})
			errs := make(chan error, 20)
			relayDone := make(chan struct{})
			go func() {
				defer close(relayDone)
				a, err := relay.Accept()
				if err != nil {
					return
				}
				defer a.Close()
				a.SetDeadline(time.Now().Add(8 * time.Second))
				first, err := readRendezvousMessage(a)
				if err != nil {
					errs <- err
					return
				}
				b, err := relay.Accept()
				if err != nil {
					return
				}
				defer b.Close()
				b.SetDeadline(time.Now().Add(8 * time.Second))
				second, err := readRendezvousMessage(b)
				if err != nil {
					errs <- err
					return
				}
				if first.GetRequestRelay().GetUuid() == "" || first.GetRequestRelay().GetUuid() != second.GetRequestRelay().GetUuid() {
					errs <- fmt.Errorf("relay UUIDs do not match")
					return
				}
				close(paired)
				relayFlowPipe(a, b)
			}()
			startPeer := func(uuid string) {
				go func() {
					c, err := net.DialTimeout("tcp", relayAddr, time.Second)
					if err != nil {
						errs <- err
						return
					}
					defer c.Close()
					c.SetDeadline(time.Now().Add(8 * time.Second))
					if err := writeRendezvousMessage(c, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RequestRelay{
						RequestRelay: &pb.RequestRelay{Uuid: uuid},
					}}); err != nil {
						errs <- err
						return
					}
					select {
					case <-paired:
					case <-time.After(5 * time.Second):
						return
					}
					p, err := net.DialTimeout("tcp", peerAddr.String(), time.Second)
					if err != nil {
						errs <- err
						return
					}
					defer p.Close()
					p.SetDeadline(time.Now().Add(8 * time.Second))
					relayFlowPipe(c, p)
				}()
			}
			signedPk := mustSignIDPk(t, serverPriv, "relay-peer", peerPub)
			go func() {
				for {
					c, err := rv.Accept()
					if err != nil {
						return
					}
					go func() {
						defer c.Close()
						c.SetDeadline(time.Now().Add(5 * time.Second))
						read := func() (*pb.RendezvousMessage, error) { return readRendezvousMessage(c) }
						write := func(msg *pb.RendezvousMessage) error { return writeRendezvousMessage(c, msg) }
						if tc.token {
							sc, err := mockAuthenticatedRendezvous(c, serverPriv)
							if err != nil {
								errs <- err
								return
							}
							read = func() (*pb.RendezvousMessage, error) { return readRendezvousSecure(sc) }
							write = func(msg *pb.RendezvousMessage) error { return writeRendezvousSecure(sc, msg) }
						}
						msg, err := read()
						if err != nil {
							errs <- err
							return
						}
						if msg.GetTestNatRequest() != nil {
							write(&pb.RendezvousMessage{Union: &pb.RendezvousMessage_TestNatResponse{TestNatResponse: &pb.TestNatResponse{Port: 12345}}})
							return
						}
						if ph := msg.GetPunchHoleRequest(); ph != nil {
							if ph.GetForceRelay() != tc.force || ph.GetConnType() != pb.ConnType_TERMINAL || (tc.token && ph.GetToken() != "test-token") {
								errs <- fmt.Errorf("incorrect punch flags/type/token")
								return
							}
							announced := relayAddr
							if tc.derive {
								announced = ""
							}
							if tc.override {
								announced = "127.0.0.1:1"
							}
							if tc.assigned {
								write(&pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{RelayResponse: &pb.RelayResponse{
									RelayServer: announced, Uuid: "assigned-uuid", Union: &pb.RelayResponse_Pk{Pk: signedPk},
								}}})
								if !tc.override {
									startPeer("assigned-uuid")
								}
							} else {
								write(&pb.RendezvousMessage{Union: &pb.RendezvousMessage_PunchHoleResponse{PunchHoleResponse: &pb.PunchHoleResponse{
									SocketAddr: EncodeAddr(netip.MustParseAddrPort("127.0.0.1:1")), Pk: signedPk, RelayServer: announced,
									Union: &pb.PunchHoleResponse_IsLocal{IsLocal: true},
								}}})
							}
							return
						}
						if rr := msg.GetRequestRelay(); rr != nil {
							if rr.GetRelayServer() != relayAddr || rr.GetUuid() == "" || rr.GetUuid() == "assigned-uuid" || !rr.GetSecure() || rr.GetConnType() != pb.ConnType_TERMINAL || (tc.token && rr.GetToken() != "test-token") {
								errs <- fmt.Errorf("incorrect coordinated relay request: %v", rr)
								return
							}
							ack := &pb.RelayResponse{}
							if tc.echo {
								ack.RelayServer = rr.GetRelayServer()
								ack.Uuid = rr.GetUuid()
							}
							if err := write(&pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{RelayResponse: ack}}); err != nil {
								errs <- err
								return
							}
							startPeer(rr.GetUuid())
						}
					}()
				}
			}()
			opts := DialOpts{Server: rv.Addr().String(), PeerID: "relay-peer", Password: "pw", Key: base64.StdEncoding.EncodeToString(serverPub), ForceRelay: tc.force, Timeout: 3 * time.Second}
			if tc.override {
				opts.RelayServer = relayAddr
			}
			if tc.token {
				opts.Token = "test-token"
			}
			ts, err := OpenTerminal(opts, 24, 80)
			if err != nil {
				select {
				case fixtureErr := <-errs:
					t.Fatalf("OpenTerminal: %v; fixture: %v", err, fixtureErr)
				default:
					t.Fatalf("OpenTerminal: %v", err)
				}
			}
			defer ts.Close()
			if !ts.conn.sc.IsSecured() || ts.conn.Direct() {
				t.Fatal("expected encrypted relay session")
			}
			input := []byte("whoami\n")
			if _, err := ts.Write(input); err != nil {
				t.Fatal(err)
			}
			out := make([]byte, len(input))
			if _, err := io.ReadFull(ts, out); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, input) {
				t.Fatalf("terminal echo: %q", out)
			}
			ts.Close()
			for _, done := range []<-chan struct{}{peerDone, relayDone} {
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("relay/peer did not finish")
				}
			}
			select {
			case err := <-errs:
				t.Fatal(err)
			default:
			}
		})
	}
}

func TestRelayRefusal(t *testing.T) {
	rv, relay := relayFlowListeners(t)
	go func() {
		c, err := rv.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err := readRendezvousMessage(c); err != nil {
			return
		}
		writeRendezvousMessage(c, &pb.RendezvousMessage{Union: &pb.RendezvousMessage_RelayResponse{RelayResponse: &pb.RelayResponse{RefuseReason: "permission denied"}}})
	}()
	_, _, err := requestRelayViaRendezvous(DialOpts{Server: rv.Addr().String(), PeerID: "peer"}, relay.Addr().String(), true, time.Second)
	if err == nil || err.Error() != "permission denied" {
		t.Fatalf("refusal not propagated: %v", err)
	}
}
