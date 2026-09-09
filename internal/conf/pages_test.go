package conf

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitPagesSettings(t *testing.T) {
	orig := Server
	t.Cleanup(func() { Server = orig })

	mustURL := func(raw string) *url.URL {
		t.Helper()
		u, err := url.Parse(raw)
		require.NoError(t, err)
		return u
	}

	tests := []struct {
		name     string
		external string
		domain   string
		protocol string
		wantErr  string
	}{
		{
			name:     "disabled",
			external: "https://gogs.example.com/",
		},
		{
			name:     "dedicated domain",
			external: "https://gogs.example.com/",
			domain:   "pages.example.com",
			protocol: "https",
		},
		{
			name:     "pages.localhost next to gogs.localhost",
			external: "https://gogs.localhost/",
			domain:   "pages.localhost",
		},
		{
			name:     "equal to application host",
			external: "https://gogs.example.com/",
			domain:   "gogs.example.com",
			wantErr:  "must not be the application host",
		},
		{
			name:     "parent of application host",
			external: "https://gogs.example.com/",
			domain:   "example.com",
			wantErr:  "would capture the application host",
		},
		{
			name:     "invalid protocol",
			external: "https://gogs.example.com/",
			domain:   "pages.example.com",
			protocol: "javascript",
			wantErr:  `must be "http" or "https"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			Server.URL = mustURL(test.external)
			Server.PagesDomain = test.domain
			Server.PagesProtocol = test.protocol

			err := initPagesSettings()
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), test.wantErr)
		})
	}
}
