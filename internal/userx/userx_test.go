package userx

import (
	"crypto/sha256"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/pbkdf2"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/osx"
	"gogs.io/gogs/internal/tool"
	"gogs.io/gogs/public"
)

func TestDashboardURLPath(t *testing.T) {
	t.Run("user", func(t *testing.T) {
		got := DashboardURLPath("alice", false)
		want := "/"
		assert.Equal(t, want, got)
	})

	t.Run("organization", func(t *testing.T) {
		got := DashboardURLPath("acme", true)
		want := "/org/acme/dashboard/"
		assert.Equal(t, want, got)
	})
}

func TestGenerateActivateCode(t *testing.T) {
	conf.SetMockAuth(t,
		conf.AuthOpts{
			ActivateCodeLives: 10,
		},
	)

	code := GenerateActivateCode(1, "alice@example.com", "Alice", "123456", "rands")
	got := tool.VerifyTimeLimitCode("1alice@example.comalice123456rands", conf.Auth.ActivateCodeLives, code[:tool.TimeLimitCodeLength])
	assert.True(t, got)
}

func TestCustomAvatarPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping testing on Windows")
		return
	}

	conf.SetMockPicture(t,
		conf.PictureOpts{
			AvatarUploadPath: "data/avatars",
		},
	)

	got := CustomAvatarPath(1)
	want := "data/avatars/1"
	assert.Equal(t, want, got)
}

func TestGenerateRandomAvatar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping testing on Windows")
		return
	}

	conf.SetMockPicture(t,
		conf.PictureOpts{
			AvatarUploadPath: os.TempDir(),
		},
	)

	avatarPath := CustomAvatarPath(1)
	defer func() { _ = os.Remove(avatarPath) }()

	err := GenerateRandomAvatar(1, "alice", "alice@example.com")
	require.NoError(t, err)
	got := osx.IsFile(avatarPath)
	assert.True(t, got)
}

func TestSaveAvatar(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping testing on Windows")
		return
	}

	conf.SetMockPicture(t,
		conf.PictureOpts{
			AvatarUploadPath: os.TempDir(),
		},
	)

	avatar, err := public.Files.ReadFile("img/avatar_default.png")
	require.NoError(t, err)

	avatarPath := CustomAvatarPath(1)
	defer func() { _ = os.Remove(avatarPath) }()

	err = SaveAvatar(1, avatar)
	require.NoError(t, err)
	got := osx.IsFile(avatarPath)
	assert.True(t, got)
}

func TestEncodePassword(t *testing.T) {
	encoded, err := EncodePassword("123456")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(encoded, "$argon2id$"))

	// Every call uses a fresh random salt, so the output must never repeat.
	other, err := EncodePassword("123456")
	require.NoError(t, err)
	assert.NotEqual(t, encoded, other)

	assert.False(t, PasswordNeedsUpgrade(encoded))
}

func TestValidatePassword(t *testing.T) {
	t.Run("argon2id round trip", func(t *testing.T) {
		encoded, err := EncodePassword("123456")
		require.NoError(t, err)

		assert.True(t, ValidatePassword(encoded, "", "123456"))
		assert.False(t, ValidatePassword(encoded, "", "111333"))
		// The user salt column is not consulted for argon2id hashes.
		assert.True(t, ValidatePassword(encoded, "ignored-salt", "123456"))
	})

	t.Run("legacy PBKDF2 hex still verifies", func(t *testing.T) {
		// A hash produced by the pre-Argon2id scheme (PBKDF2-HMAC-SHA256,
		// 10000 iterations, 50-byte key) for password "123456" and salt "rands".
		legacy := fmt.Sprintf("%x", pbkdf2.Key([]byte("123456"), []byte("rands"), 10000, 50, sha256.New))

		assert.True(t, ValidatePassword(legacy, "rands", "123456"))
		assert.False(t, ValidatePassword(legacy, "rands", "111333"))
		assert.False(t, ValidatePassword(legacy, "wrong-salt", "123456"))
		assert.True(t, PasswordNeedsUpgrade(legacy))
	})

	t.Run("malformed argon2id hash is rejected", func(t *testing.T) {
		assert.False(t, ValidatePassword("$argon2id$v=19$m=19456,t=2,p=1$bad$bad", "", "123456"))
		assert.False(t, ValidatePassword("$argon2id$garbage", "", "123456"))
	})
}

func TestMailResendCacheKey(t *testing.T) {
	got := MailResendCacheKey(1)
	assert.Equal(t, "mailResend::1", got)
}

func TestTwoFactorCacheKey(t *testing.T) {
	got := TwoFactorCacheKey(1, "113654")
	assert.Equal(t, "twoFactor::1::113654", got)
}

func TestRandomSalt(t *testing.T) {
	salt1, err := RandomSalt()
	require.NoError(t, err)
	salt2, err := RandomSalt()
	require.NoError(t, err)
	assert.NotEqual(t, salt1, salt2)
}
