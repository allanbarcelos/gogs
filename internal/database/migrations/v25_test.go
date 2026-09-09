package migrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/dbtest"
)

func TestEncryptCommitStatusSecretsAndDisableExisting(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	orig := conf.Security.SecretKey
	conf.Security.SecretKey = "test-secret-key"
	t.Cleanup(func() { conf.Security.SecretKey = orig })

	db := dbtest.NewDB(t, "encryptCommitStatusSecrets", new(repoV25))
	for _, repo := range []*repoV25{
		{ID: 1, EnableCommitStatus: true, CommitStatusSecret: "abcdefghijklmnopqrstuvwxyz0123456789ABCD"},
		{ID: 2, EnableCommitStatus: true, CommitStatusSecret: ""},
	} {
		require.NoError(t, db.Create(repo).Error)
	}

	err := encryptCommitStatusSecretsAndDisableExisting(db)
	require.NoError(t, err)

	var repos []repoV25
	err = db.Order("id").Find(&repos).Error
	require.NoError(t, err)
	require.Len(t, repos, 2)

	assert.False(t, repos[0].EnableCommitStatus)
	assert.False(t, repos[1].EnableCommitStatus)
	assert.NotEqual(t, "abcdefghijklmnopqrstuvwxyz0123456789ABCD", repos[0].CommitStatusSecret)
	assert.Greater(t, len(repos[0].CommitStatusSecret), 40)
	assert.Empty(t, repos[1].CommitStatusSecret)
}
