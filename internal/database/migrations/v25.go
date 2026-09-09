package migrations

import (
	"encoding/base64"
	"unicode"

	"gorm.io/gorm"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/cryptox"
)

type repoV25 struct {
	ID                 int64  `gorm:"primaryKey"`
	EnableCommitStatus bool   `gorm:"column:enable_commit_status"`
	CommitStatusSecret string `gorm:"column:commit_status_secret;type:TEXT"`
}

func (*repoV25) TableName() string {
	return "repository"
}

func encryptCommitStatusSecretsAndDisableExisting(db *gorm.DB) error {
	if !db.Migrator().HasTable(&repoV25{}) {
		return errMigrationSkipped
	}
	if !db.Migrator().HasColumn(&repoV25{}, "CommitStatusSecret") {
		return errMigrationSkipped
	}

	if err := db.Migrator().AlterColumn(&repoV25{}, "CommitStatusSecret"); err != nil {
		return err
	}

	var repos []repoV25
	if err := db.Find(&repos).Error; err != nil {
		return err
	}
	for i := range repos {
		stored := repos[i].CommitStatusSecret
		if stored == "" || !isLegacyPlainSecret(stored) {
			continue
		}
		encrypted, err := cryptox.AESGCMEncrypt(cryptox.MD5Bytes(conf.Security.SecretKey), []byte(stored))
		if err != nil {
			return err
		}
		err = db.Model(&repos[i]).Update("commit_status_secret", base64.StdEncoding.EncodeToString(encrypted)).Error
		if err != nil {
			return err
		}
	}

	return db.Model(new(repoV25)).Where("1 = 1").Update("enable_commit_status", false).Error
}

func isLegacyPlainSecret(stored string) bool {
	if len(stored) != 40 {
		return false
	}
	for _, r := range stored {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
