package database

import (
	"context"
	"fmt"

	"github.com/cockroachdb/errors"
	"gorm.io/gorm"

	"gogs.io/gogs/internal/errx"
)

// RepoPage is the Gogs Pages publishing configuration for a repository. It holds
// at most one row per repository.
type RepoPage struct {
	ID          int64  `gorm:"primaryKey"`
	RepoID      int64  `gorm:"uniqueIndex;not null"`
	Branch      string `gorm:"not null"` // The branch the static site is served from.
	Dir         string `gorm:"not null"` // The directory within the branch, either "/" or "/docs".
	Enabled     bool   `gorm:"not null"`
	CreatedUnix int64
	UpdatedUnix int64
}

// BeforeCreate implements the GORM create hook.
func (p *RepoPage) BeforeCreate(tx *gorm.DB) error {
	if p.CreatedUnix == 0 {
		p.CreatedUnix = tx.NowFunc().Unix()
	}
	if p.UpdatedUnix == 0 {
		p.UpdatedUnix = p.CreatedUnix
	}
	return nil
}

// PagesStore is the storage layer for repositories' Gogs Pages configuration.
type PagesStore struct {
	db *gorm.DB
}

func newPagesStore(db *gorm.DB) *PagesStore {
	return &PagesStore{db: db}
}

var _ errx.NotFound = (*ErrRepoPageNotFound)(nil)

type ErrRepoPageNotFound struct {
	args errx.Args
}

func IsErrRepoPageNotFound(err error) bool {
	return errors.As(err, &ErrRepoPageNotFound{})
}

func (err ErrRepoPageNotFound) Error() string {
	return fmt.Sprintf("repository pages configuration does not exist: %v", err.args)
}

func (ErrRepoPageNotFound) NotFound() bool {
	return true
}

// Get returns the Pages configuration of the given repository. It returns
// ErrRepoPageNotFound when the repository has never configured Pages.
func (s *PagesStore) Get(ctx context.Context, repoID int64) (*RepoPage, error) {
	page := new(RepoPage)
	err := s.db.WithContext(ctx).Where("repo_id = ?", repoID).First(page).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrRepoPageNotFound{args: errx.Args{"repoID": repoID}}
		}
		return nil, err
	}
	return page, nil
}

type SavePagesOptions struct {
	Enabled bool
	Branch  string
	Dir     string
}

// Save creates or updates the Pages configuration of the given repository.
func (s *PagesStore) Save(ctx context.Context, repoID int64, opts SavePagesOptions) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var page RepoPage
		err := tx.Where("repo_id = ?", repoID).First(&page).Error
		if err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			return tx.Create(&RepoPage{
				RepoID:  repoID,
				Branch:  opts.Branch,
				Dir:     opts.Dir,
				Enabled: opts.Enabled,
			}).Error
		}

		return tx.Model(&page).Updates(map[string]any{
			"branch":       opts.Branch,
			"dir":          opts.Dir,
			"enabled":      opts.Enabled,
			"updated_unix": tx.NowFunc().Unix(),
		}).Error
	})
}

// Disable turns off Pages serving for the given repository while keeping the
// stored branch and directory for next time. It is a no-op when the repository
// has no Pages configuration.
func (s *PagesStore) Disable(ctx context.Context, repoID int64) error {
	return s.db.WithContext(ctx).
		Model(new(RepoPage)).
		Where("repo_id = ?", repoID).
		Updates(map[string]any{
			"enabled":      false,
			"updated_unix": s.db.NowFunc().Unix(),
		}).Error
}
