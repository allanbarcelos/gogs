package migrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/dbtest"
)

func TestBackfillCommitStatusSecrets(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	db := dbtest.NewDB(t, "backfillCommitStatusSecrets", new(repoV24))
	for _, repo := range []*repoV24{
		{ID: 1, EnableCommitStatus: true, CommitStatusSecret: ""},
		{ID: 2, EnableCommitStatus: true, CommitStatusSecret: "already-set-secret-value-0123456789"},
		{ID: 3, EnableCommitStatus: false, CommitStatusSecret: ""},
	} {
		require.NoError(t, db.Create(repo).Error)
	}

	err := backfillCommitStatusSecrets(db)
	require.NoError(t, err)

	var repos []repoV24
	err = db.Order("id").Find(&repos).Error
	require.NoError(t, err)
	require.Len(t, repos, 3)

	assert.Len(t, repos[0].CommitStatusSecret, 40)
	assert.Equal(t, "already-set-secret-value-0123456789", repos[1].CommitStatusSecret)
	assert.Empty(t, repos[2].CommitStatusSecret)
}
