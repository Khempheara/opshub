package tlsprobe

import (
	"errors"
	"net"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestShortErr(t *testing.T) {
	assert.Equal(t, "host not found", ShortErr(&net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x.invalid", Server: "127.0.0.11:53", IsNotFound: true}}))
	assert.Equal(t, "DNS lookup timed out", ShortErr(&net.DNSError{IsTimeout: true, Server: "10.0.0.2:53"}))
	assert.Equal(t, "DNS lookup failed", ShortErr(&net.DNSError{Err: "server misbehaving", Server: "10.0.0.2:53"}))
	assert.Equal(t, "connection refused", ShortErr(&net.OpError{Op: "dial", Addr: &net.TCPAddr{Port: 1}, Err: errors.New("connection refused")}))
	assert.Equal(t, "connection refused", ShortErr(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}))
	assert.Equal(t, "boom", ShortErr(errors.New("boom")))
}
