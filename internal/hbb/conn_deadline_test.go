package hbb

import (
	"errors"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	pb "rdcli/internal/hbb/proto"
)

func TestLoginSessionOutlivesSetupDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		client.SetDeadline(time.Now().Add(time.Second))
		conn := &Conn{sc: NewSecureConn(client), opts: DialOpts{Timeout: time.Second}}
		peer := &Conn{sc: NewSecureConn(server)}
		peerDone := make(chan error, 1)
		go func() {
			if err := peer.Send(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}}); err != nil {
				peerDone <- err
				return
			}
			if _, err := peer.Recv(); err != nil {
				peerDone <- err
				return
			}
			if err := peer.Send(&pb.Message{Union: &pb.Message_LoginResponse{LoginResponse: &pb.LoginResponse{
				Union: &pb.LoginResponse_PeerInfo{PeerInfo: &pb.PeerInfo{Version: "1.4.1"}},
			}}}); err != nil {
				peerDone <- err
				return
			}
			time.Sleep(2 * time.Minute)
			msg, err := peer.Recv()
			if err == nil {
				err = peer.Send(msg)
			}
			peerDone <- err
		}()

		if _, err := conn.Login(LoginTerminal, LoginSpec{}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Minute)
		if err := conn.Send(&pb.Message{Union: &pb.Message_TestDelay{TestDelay: &pb.TestDelay{Time: 42}}}); err != nil {
			t.Fatalf("write after setup deadline: %v", err)
		}
		msg, err := conn.Recv()
		if err != nil {
			t.Fatalf("read after setup deadline: %v", err)
		}
		if msg.GetTestDelay().GetTime() != 42 {
			t.Fatalf("unexpected reply: %v", msg)
		}
		if err := <-peerDone; err != nil {
			t.Fatal(err)
		}
	})
}

func TestLoginStillTimesOutDuringSetup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		client.SetDeadline(time.Now().Add(time.Second))
		conn := &Conn{sc: NewSecureConn(client), opts: DialOpts{Timeout: time.Second}}
		_, err := conn.Login(LoginTerminal, LoginSpec{})
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("login error = %v, want timeout", err)
		}
	})
}

func TestLoginTimeoutExplainsMissingDevicePassword(t *testing.T) {
	for _, password := range []string{"", "supplied-password"} {
		synctest.Test(t, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			client.SetDeadline(time.Now().Add(time.Second))
			conn := &Conn{sc: NewSecureConn(client), opts: DialOpts{Password: password}}
			peer := &Conn{sc: NewSecureConn(server)}
			peerDone := make(chan error, 1)
			go func() {
				if err := peer.Send(&pb.Message{Union: &pb.Message_Hash{Hash: &pb.Hash{Salt: "salt", Challenge: "challenge"}}}); err != nil {
					peerDone <- err
					return
				}
				_, err := peer.Recv()
				peerDone <- err
			}()
			_, err := conn.Login(LoginTerminal, LoginSpec{})
			var timeout net.Error
			if !errors.As(err, &timeout) || !timeout.Timeout() {
				t.Fatalf("error=%v, want preserved timeout", err)
			}
			if strings.Contains(err.Error(), "RDC_PASSWORD") != (password == "") {
				t.Fatalf("missing-password hint incorrect: %v", err)
			}
			if password != "" && strings.Contains(err.Error(), password) {
				t.Fatal("device password leaked into error")
			}
			if err := <-peerDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
