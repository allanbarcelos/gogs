package pages

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"gogs.io/gogs/internal/conf"
)

func TestStripPort(t *testing.T) {
	assert.Equal(t, "alice.pages.example.com", stripPort("alice.pages.example.com"))
	assert.Equal(t, "alice.pages.example.com", stripPort("alice.pages.example.com:3000"))
	assert.Equal(t, "", stripPort(""))
}

func TestOwnerFromHost(t *testing.T) {
	t.Run("feature disabled", func(t *testing.T) {
		conf.Server.PagesDomain = ""
		_, ok := ownerFromHost("alice.pages.example.com")
		assert.False(t, ok)
	})

	conf.Server.PagesDomain = "pages.example.com"
	t.Cleanup(func() { conf.Server.PagesDomain = "" })

	t.Run("application host", func(t *testing.T) {
		_, ok := ownerFromHost("gogs.example.com")
		assert.False(t, ok)
	})

	t.Run("apex pages domain", func(t *testing.T) {
		_, ok := ownerFromHost("pages.example.com")
		assert.False(t, ok)
	})

	t.Run("nested label is not a valid owner", func(t *testing.T) {
		_, ok := ownerFromHost("a.b.pages.example.com")
		assert.False(t, ok)
	})

	t.Run("valid owner", func(t *testing.T) {
		owner, ok := ownerFromHost("alice.pages.example.com")
		assert.True(t, ok)
		assert.Equal(t, "alice", owner)
	})

	t.Run("owner label is lower-cased", func(t *testing.T) {
		owner, ok := ownerFromHost("Alice.pages.example.com")
		assert.True(t, ok)
		assert.Equal(t, "alice", owner)
	})
}
