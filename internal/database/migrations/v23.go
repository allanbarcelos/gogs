package migrations

import (
	"gorm.io/gorm"
)

type commitStatusV23 struct {
	CreatorName string
}

func (*commitStatusV23) TableName() string {
	return "commit_status"
}

func addCommitStatusCreatorName(db *gorm.DB) error {
	if !db.Migrator().HasTable(&commitStatusV23{}) {
		return errMigrationSkipped
	}
	if db.Migrator().HasColumn(&commitStatusV23{}, "CreatorName") {
		return errMigrationSkipped
	}
	return db.Migrator().AddColumn(&commitStatusV23{}, "CreatorName")
}
