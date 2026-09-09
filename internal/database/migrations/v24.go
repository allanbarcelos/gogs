package migrations

import (
	"gorm.io/gorm"

	"gogs.io/gogs/internal/strx"
)

type repoV24 struct {
	ID                 int64  `gorm:"primaryKey"`
	EnableCommitStatus bool   `gorm:"column:enable_commit_status"`
	CommitStatusSecret string `gorm:"column:commit_status_secret;type:VARCHAR(40)"`
}

func (*repoV24) TableName() string {
	return "repository"
}

func backfillCommitStatusSecrets(db *gorm.DB) error {
	if !db.Migrator().HasTable(&repoV24{}) {
		return errMigrationSkipped
	}
	if !db.Migrator().HasColumn(&repoV24{}, "CommitStatusSecret") {
		return errMigrationSkipped
	}

	var repos []repoV24
	err := db.Where("enable_commit_status = ? AND (commit_status_secret IS NULL OR commit_status_secret = ?)", true, "").
		Find(&repos).Error
	if err != nil {
		return err
	}
	for i := range repos {
		secret, err := strx.RandomChars(40)
		if err != nil {
			return err
		}
		err = db.Model(&repos[i]).Update("commit_status_secret", secret).Error
		if err != nil {
			return err
		}
	}
	return nil
}
