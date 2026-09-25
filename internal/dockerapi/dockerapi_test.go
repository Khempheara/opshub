package dockerapi

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDemux(t *testing.T) {
	var in bytes.Buffer
	for _, f := range []struct {
		stream byte
		data   string
	}{{1, "out\n"}, {2, "err\n"}, {1, "more"}} {
		hdr := make([]byte, 8)
		hdr[0] = f.stream
		binary.BigEndian.PutUint32(hdr[4:], uint32(len(f.data)))
		in.Write(hdr)
		in.WriteString(f.data)
	}
	var out bytes.Buffer
	require.NoError(t, Demux(&in, &out))
	assert.Equal(t, "out\nerr\nmore", out.String())
}

func TestSplitImage(t *testing.T) {
	for in, want := range map[string][2]string{
		"alpine":                    {"alpine", "latest"},
		"alpine:3.20":               {"alpine", "3.20"},
		"registry:5000/team/app":    {"registry:5000/team/app", "latest"},
		"registry:5000/team/app:v1": {"registry:5000/team/app", "v1"},
		"alpine@sha256:abc":         {"alpine@sha256:abc", ""},
	} {
		ref, tag := SplitImage(in)
		assert.Equal(t, want, [2]string{ref, tag}, in)
	}
	assert.True(t, VersionLess("1.41", "1.47"))
	assert.False(t, VersionLess("1.47", "1.47"))
	assert.False(t, VersionLess("2.0", "1.47"))
}

func TestPortBindings(t *testing.T) {
	exposed, bindings, err := portBindings([]string{"8080:80", "127.0.0.1:9090:90"})
	require.NoError(t, err)
	assert.Contains(t, exposed, "80/tcp")
	assert.Equal(t, []map[string]string{{"HostPort": "8080"}}, bindings["80/tcp"])
	assert.Equal(t, []map[string]string{{"HostPort": "9090", "HostIp": "127.0.0.1"}}, bindings["90/tcp"])
	_, _, err = portBindings([]string{"80"})
	assert.Error(t, err)
}
