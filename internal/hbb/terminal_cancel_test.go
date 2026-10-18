package hbb

import (
	"net"
	"testing"
	"testing/synctest"
	"time"

	pb "rdcli/internal/hbb/proto"
)

func TestTerminalAbortInterruptsReadAndWrite(t *testing.T) {
	for _, action := range []string{"read", "write"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, server := net.Pipe()
				defer client.Close()
				defer server.Close()
				pi := &pb.PeerInfo{Platform: "Windows", Version: "1.4.1"}
				ts := &TerminalSession{conn: &Conn{sc: NewSecureConn(client), peerInfo: pi}}
				if ts.PeerInfo() != pi {
					t.Fatal("terminal lost authenticated peer info")
				}
				done := make(chan error, 1)
				go func() {
					var err error
					if action == "read" {
						_, err = ts.Read(make([]byte, 64))
					} else {
						_, err = ts.Write([]byte("command\r"))
					}
					done <- err
				}()
				synctest.Wait()
				if err := ts.Abort(); err != nil {
					t.Fatal(err)
				}
				if err := <-done; err == nil {
					t.Fatal("blocked operation succeeded after abort")
				}
				if _, err := ts.Read(make([]byte, 1)); err == nil {
					t.Fatal("read after abort unexpectedly succeeded")
				}
			})
		})
	}
}

func TestTerminalCloseDoesNotBlockOnUnresponsivePeer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		ts := &TerminalSession{conn: &Conn{sc: NewSecureConn(client)}}
		start := time.Now()
		if err := ts.Close(); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed != time.Second {
			t.Fatalf("close took %v, want bounded one second", elapsed)
		}
	})
}
