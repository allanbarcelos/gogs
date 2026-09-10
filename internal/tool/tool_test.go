package tool

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"gogs.io/gogs/internal/conf"
)

func TestTimeLimitCode(t *testing.T) {
	orig := conf.Security.SecretKey
	conf.Security.SecretKey = "test-secret-key"
	t.Cleanup(func() { conf.Security.SecretKey = orig })

	const data = "1alice@example.comalice$argon2id$hashrands"

	t.Run("round trip within lifetime", func(t *testing.T) {
		code := CreateTimeLimitCode(data, 10, nil)
		assert.Len(t, code, TimeLimitCodeLength)
		assert.True(t, VerifyTimeLimitCode(data, 10, code))
	})

	t.Run("tampered digest is rejected", func(t *testing.T) {
		code := CreateTimeLimitCode(data, 10, nil)
		tampered := code[:len(code)-1] + "0"
		if tampered == code {
			tampered = code[:len(code)-1] + "1"
		}
		assert.False(t, VerifyTimeLimitCode(data, 10, tampered))
	})

	t.Run("different payload is rejected", func(t *testing.T) {
		code := CreateTimeLimitCode(data, 10, nil)
		assert.False(t, VerifyTimeLimitCode(data+"x", 10, code))
	})

	t.Run("different secret is rejected", func(t *testing.T) {
		code := CreateTimeLimitCode(data, 10, nil)
		conf.Security.SecretKey = "another-secret"
		defer func() { conf.Security.SecretKey = "test-secret-key" }()
		assert.False(t, VerifyTimeLimitCode(data, 10, code))
	})

	t.Run("expired code is rejected", func(t *testing.T) {
		start := time.Now().Add(-20 * time.Minute).Format("200601021504")
		code := CreateTimeLimitCode(data, 10, start)
		assert.False(t, VerifyTimeLimitCode(data, 10, code))
	})
}
