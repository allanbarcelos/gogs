package netx

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsLocalHostname(t *testing.T) {
	tests := []struct {
		hostname  string
		allowlist []string
		want      bool
	}{
		{hostname: "localhost", want: true},       // #00
		{hostname: "127.0.0.1", want: true},       // #01
		{hostname: "::1", want: true},             // #02
		{hostname: "0:0:0:0:0:0:0:1", want: true}, // #03
		{hostname: "127.0.0.95", want: true},      // #04
		{hostname: "0.0.0.0", want: true},         // #05
		{hostname: "192.168.123.45", want: true},  // #06

		{hostname: "gogs.io", want: false},         // #07
		{hostname: "google.com", want: false},      // #08
		{hostname: "165.232.140.255", want: false}, // #09

		{hostname: "192.168.123.45", allowlist: []string{"10.0.0.17"}, want: true}, // #10
		{hostname: "gogs.local", allowlist: []string{"gogs.local"}, want: false},   // #11

		{hostname: "192.168.123.45", allowlist: []string{"*"}, want: false}, // #12
	}
	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			assert.Equal(t, test.want, IsBlockedLocalHostname(test.hostname, test.allowlist))
		})
	}
}

func TestAllowlistPermitsIP(t *testing.T) {
	ip := net.ParseIP("10.0.0.17")
	assert.False(t, allowlistPermitsIP(ip, nil))
	assert.False(t, allowlistPermitsIP(ip, []string{"10.0.0.18", "example.com"}))
	assert.True(t, allowlistPermitsIP(ip, []string{"*"}))
	assert.True(t, allowlistPermitsIP(ip, []string{"10.0.0.17"}))
	assert.True(t, allowlistPermitsIP(ip, []string{"10.0.0.0/8"}))
	assert.False(t, allowlistPermitsIP(ip, []string{"192.168.0.0/16"}))
}

func TestSafeDialContext(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	require.NoError(t, err)

	call := func(allowlist []string, host string) error {
		client := &http.Client{
			Transport: SafeHTTPTransport(allowlist, 5*time.Second, 5*time.Second),
		}
		resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	t.Run("blocks loopback by default", func(t *testing.T) {
		assert.Error(t, call(nil, "127.0.0.1"))
	})

	t.Run("blocks a hostname that resolves to loopback", func(t *testing.T) {
		assert.Error(t, call(nil, "localhost"))
	})

	t.Run("wildcard allowlist permits it", func(t *testing.T) {
		assert.NoError(t, call([]string{"*"}, "127.0.0.1"))
	})

	t.Run("explicit IP allowlist permits it", func(t *testing.T) {
		assert.NoError(t, call([]string{"127.0.0.1"}, "127.0.0.1"))
	})

	t.Run("CIDR allowlist permits it", func(t *testing.T) {
		assert.NoError(t, call([]string{"127.0.0.0/8"}, "127.0.0.1"))
	})

	t.Run("hostname allowlist entry permits it", func(t *testing.T) {
		assert.NoError(t, call([]string{"localhost"}, "localhost"))
	})
}
