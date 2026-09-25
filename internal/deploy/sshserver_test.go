package deploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"net"
	"os/exec"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

// sshServer is a minimal SSH server for tests: it accepts one client key and runs "exec"
// requests with the local /bin/sh.
type sshServer struct {
	addr        string
	fingerprint string
	clientKey   string // PEM private key the server accepts
	mu          sync.Mutex
	commands    []string
}

func (s *sshServer) ran() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

func startSSHServer(t *testing.T) *sshServer {
	t.Helper()
	_, hostPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	hostSigner, err := ssh.NewSignerFromKey(hostPriv)
	require.NoError(t, err)
	clientPub, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	block, err := ssh.MarshalPrivateKey(clientPriv, "")
	require.NoError(t, err)
	allowed, err := ssh.NewPublicKey(clientPub)
	require.NoError(t, err)

	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if string(key.Marshal()) == string(allowed.Marshal()) {
				return nil, nil
			}
			return nil, ssh.ErrNoAuth
		},
	}
	cfg.AddHostKey(hostSigner)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	s := &sshServer{addr: ln.Addr().String(), fingerprint: ssh.FingerprintSHA256(hostSigner.PublicKey()), clientKey: string(pem.EncodeToMemory(block))}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *sshServer) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer func() { _ = ch.Close() }()
			for req := range requests {
				if req.Type != "exec" {
					_ = req.Reply(req.Type == "signal", nil)
					continue
				}
				var payload struct{ Command string }
				_ = ssh.Unmarshal(req.Payload, &payload)
				_ = req.Reply(true, nil)
				s.mu.Lock()
				s.commands = append(s.commands, payload.Command)
				s.mu.Unlock()
				cmd := exec.Command("/bin/sh", "-c", payload.Command) // #nosec G204 -- test server
				cmd.Stdout, cmd.Stderr = ch, ch.Stderr()
				code := 0
				if err := cmd.Run(); err != nil {
					code = 1
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						code = ee.ExitCode()
					}
				}
				status := make([]byte, 4)
				binary.BigEndian.PutUint32(status, uint32(code)) // #nosec G115 -- exit codes are small
				_, _ = ch.SendRequest("exit-status", false, status)
				return
			}
		}()
	}
}
