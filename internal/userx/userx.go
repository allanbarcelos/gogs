package userx

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/image/draw"

	"gogs.io/gogs/internal/avatar"
	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/strx"
	"gogs.io/gogs/internal/tool"
)

// DashboardURLPath returns the URL path to the user or organization dashboard.
func DashboardURLPath(name string, isOrganization bool) string {
	if isOrganization {
		return conf.Server.Subpath + "/org/" + name + "/dashboard/"
	}
	return conf.Server.Subpath + "/"
}

// GenerateActivateCode generates an activate code based on user information and
// the given email.
func GenerateActivateCode(userID int64, email, name, password, rands string) string {
	return generateTimeLimitedUserCode(userID, email, name, password, rands, conf.Auth.ActivateCodeLives)
}

// GenerateResetPasswordCode generates a password-reset code based on user
// information and the given email. The token's lifetime is bound to
// [conf.AuthOpts.ResetPasswordCodeLives].
func GenerateResetPasswordCode(userID int64, email, name, password, rands string) string {
	return generateTimeLimitedUserCode(userID, email, name, password, rands, conf.Auth.ResetPasswordCodeLives)
}

func generateTimeLimitedUserCode(userID int64, email, name, password, rands string, minutes int) string {
	code := tool.CreateTimeLimitCode(
		fmt.Sprintf("%d%s%s%s%s", userID, email, strings.ToLower(name), password, rands),
		minutes,
		nil,
	)

	// Add tailing hex username
	code += hex.EncodeToString([]byte(strings.ToLower(name)))
	return code
}

// CustomAvatarPath returns the absolute path of the user custom avatar file.
func CustomAvatarPath(userID int64) string {
	return filepath.Join(conf.Picture.AvatarUploadPath, strconv.FormatInt(userID, 10))
}

// GenerateRandomAvatar generates a random avatar and stores to local file
// system using given user information.
func GenerateRandomAvatar(userID int64, name, email string) error {
	seed := email
	if seed == "" {
		seed = name
	}

	img, err := avatar.RandomImage([]byte(seed))
	if err != nil {
		return errors.Wrap(err, "generate random image")
	}

	avatarPath := CustomAvatarPath(userID)
	err = os.MkdirAll(filepath.Dir(avatarPath), os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "create avatar directory")
	}

	f, err := os.Create(avatarPath)
	if err != nil {
		return errors.Wrap(err, "create avatar file")
	}
	defer func() { _ = f.Close() }()

	if err = png.Encode(f, img); err != nil {
		return errors.Wrap(err, "encode avatar image to file")
	}
	return nil
}

// SaveAvatar saves the given avatar for the user.
func SaveAvatar(userID int64, data []byte) error {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return errors.Wrap(err, "decode image")
	}

	avatarPath := CustomAvatarPath(userID)
	err = os.MkdirAll(filepath.Dir(avatarPath), os.ModePerm)
	if err != nil {
		return errors.Wrap(err, "create avatar directory")
	}

	f, err := os.Create(avatarPath)
	if err != nil {
		return errors.Wrap(err, "create avatar file")
	}
	defer func() { _ = f.Close() }()

	dst := image.NewRGBA(image.Rect(0, 0, avatar.DefaultSize, avatar.DefaultSize))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), img, img.Bounds(), draw.Over, nil)
	if err = png.Encode(f, dst); err != nil {
		return errors.Wrap(err, "encode avatar image to file")
	}
	return nil
}

// Argon2id parameters for newly encoded passwords. These meet the OWASP
// minimum (19 MiB of memory, 2 iterations, 1 lane). They can be raised later
// without invalidating stored hashes because every hash records the cost it
// was created with.
const (
	argon2idMemoryKiB   = 19 * 1024
	argon2idIterations  = 2
	argon2idParallelism = 1
	argon2idSaltLength  = 16
	argon2idKeyLength   = 32
)

// argon2idPrefix marks a password hash produced by [EncodePassword]. Anything
// without it is a legacy PBKDF2-HMAC-SHA256 hex digest keyed by the user's
// separate salt column.
const argon2idPrefix = "$argon2id$"

// EncodePassword hashes password with Argon2id and returns a PHC-formatted
// string that carries the algorithm, its parameters, and a fresh random salt.
// The salt argument of the legacy signature is gone: Argon2id embeds its own.
func EncodePassword(password string) (string, error) {
	salt := make([]byte, argon2idSaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.Wrap(err, "read random salt")
	}
	key := argon2.IDKey(
		[]byte(password), salt,
		argon2idIterations, argon2idMemoryKiB, argon2idParallelism, argon2idKeyLength,
	)
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2idPrefix, argon2.Version,
		argon2idMemoryKiB, argon2idIterations, argon2idParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// ValidatePassword reports whether password matches encoded. It accepts both
// the current Argon2id format and the legacy PBKDF2 hex format, the latter
// keyed by salt. Both comparisons run in constant time.
func ValidatePassword(encoded, salt, password string) bool {
	if strings.HasPrefix(encoded, argon2idPrefix) {
		return validateArgon2idPassword(encoded, password)
	}
	return validateLegacyPassword(encoded, salt, password)
}

// PasswordNeedsUpgrade reports whether encoded uses an outdated scheme and
// should be re-hashed with [EncodePassword] after the password is next known
// in plaintext (i.e. on a successful login).
func PasswordNeedsUpgrade(encoded string) bool {
	return !strings.HasPrefix(encoded, argon2idPrefix)
}

func validateLegacyPassword(encoded, salt, password string) bool {
	got := fmt.Sprintf("%x", pbkdf2.Key([]byte(password), []byte(salt), 10000, 50, sha256.New))
	return subtle.ConstantTimeCompare([]byte(encoded), []byte(got)) == 1
}

func validateArgon2idPassword(encoded, password string) bool {
	// $argon2id$v=19$m=19456,t=2,p=1$<base64 salt>$<base64 key>
	rest, ok := strings.CutPrefix(encoded, argon2idPrefix)
	if !ok {
		return false
	}
	parts := strings.Split(rest, "$")
	if len(parts) != 4 {
		return false
	}

	var version int
	if _, err := fmt.Sscanf(parts[0], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}

	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[1], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false
	}
	if memory == 0 || iterations == 0 || parallelism == 0 {
		return false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}

	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(want, got) == 1
}

// MailResendCacheKey returns the key used for caching mail resend.
func MailResendCacheKey(userID int64) string {
	return fmt.Sprintf("mailResend::%d", userID)
}

// TwoFactorCacheKey returns the key used for caching two factor passcode.
func TwoFactorCacheKey(userID int64, passcode string) string {
	return fmt.Sprintf("twoFactor::%d::%s", userID, passcode)
}

// RandomSalt returns randomly generated 10-character string that can be used as
// the user salt.
func RandomSalt() (string, error) {
	return strx.RandomChars(10)
}
