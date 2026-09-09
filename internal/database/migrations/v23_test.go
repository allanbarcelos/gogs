package migrations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/dbtest"
)

type commitStatusPreV23 struct {
	ID        int64 `gorm:"primaryKey"`
	RepoID    int64
	CommitSHA string `gorm:"column:commit_sha"`
	State     string
	Context   string
	CreatorID int64
}

func (*commitStatusPreV23) TableName() string {
	return "commit_status"
}

func TestAddCommitStatusCreatorName(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	db := dbtest.NewDB(t, "addCommitStatusCreatorName", new(commitStatusPreV23))
	err := db.Create(&commitStatusPreV23{ID: 1, RepoID: 1, CommitSHA: "abc", State: "success", Context: "ci"}).Error
	require.NoError(t, err)
	assert.False(t, db.Migrator().HasColumn(&commitStatusV23{}, "CreatorName"))

	err = addCommitStatusCreatorName(db)
	require.NoError(t, err)
	assert.True(t, db.Migrator().HasColumn(&commitStatusV23{}, "CreatorName"))

	// Re-run is a no-op.
	err = addCommitStatusCreatorName(db)
	require.Equal(t, errMigrationSkipped, err)
}
