package database

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"gogs.io/gogs/internal/errx"
)

func TestRepoPage_BeforeCreate(t *testing.T) {
	now := int64(1588568886)
	db := &gorm.DB{
		Config: &gorm.Config{
			SkipDefaultTransaction: true,
			NowFunc: func() time.Time {
				return time.Unix(now, 0)
			},
		},
	}

	t.Run("timestamps have been set", func(t *testing.T) {
		p := &RepoPage{CreatedUnix: 1, UpdatedUnix: 2}
		_ = p.BeforeCreate(db)
		assert.Equal(t, int64(1), p.CreatedUnix)
		assert.Equal(t, int64(2), p.UpdatedUnix)
	})

	t.Run("timestamps have not been set", func(t *testing.T) {
		p := &RepoPage{}
		_ = p.BeforeCreate(db)
		assert.Equal(t, now, p.CreatedUnix)
		assert.Equal(t, now, p.UpdatedUnix)
	})
}

func TestPages(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	ctx := context.Background()
	s := &PagesStore{
		db: newTestDB(t, "PagesStore"),
	}

	for _, tc := range []struct {
		name string
		test func(t *testing.T, ctx context.Context, s *PagesStore)
	}{
		{"Get", pagesGet},
		{"Save", pagesSave},
		{"Disable", pagesDisable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() {
				err := clearTables(t, s.db)
				require.NoError(t, err)
			})
			tc.test(t, ctx, s)
		})
		if t.Failed() {
			break
		}
	}
}

func pagesGet(t *testing.T, ctx context.Context, s *PagesStore) {
	_, err := s.Get(ctx, 1)
	wantErr := ErrRepoPageNotFound{args: errx.Args{"repoID": int64(1)}}
	assert.Equal(t, wantErr, err)

	err = s.Save(ctx, 1, SavePagesOptions{Enabled: true, Branch: "main", Dir: "/"})
	require.NoError(t, err)

	page, err := s.Get(ctx, 1)
	require.NoError(t, err)
	assert.True(t, page.Enabled)
	assert.Equal(t, "main", page.Branch)
	assert.Equal(t, "/", page.Dir)
}

func pagesSave(t *testing.T, ctx context.Context, s *PagesStore) {
	err := s.Save(ctx, 1, SavePagesOptions{Enabled: true, Branch: "main", Dir: "/"})
	require.NoError(t, err)

	page, err := s.Get(ctx, 1)
	require.NoError(t, err)
	firstID := page.ID

	// Saving again updates the existing row rather than creating a new one.
	err = s.Save(ctx, 1, SavePagesOptions{Enabled: true, Branch: "gh-pages", Dir: "/docs"})
	require.NoError(t, err)

	page, err = s.Get(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, firstID, page.ID)
	assert.Equal(t, "gh-pages", page.Branch)
	assert.Equal(t, "/docs", page.Dir)

	var count int64
	err = s.db.Model(new(RepoPage)).Where("repo_id = ?", 1).Count(&count).Error
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func pagesDisable(t *testing.T, ctx context.Context, s *PagesStore) {
	// Disabling a repository that never configured Pages is a no-op.
	err := s.Disable(ctx, 1)
	require.NoError(t, err)

	err = s.Save(ctx, 1, SavePagesOptions{Enabled: true, Branch: "main", Dir: "/"})
	require.NoError(t, err)

	err = s.Disable(ctx, 1)
	require.NoError(t, err)

	page, err := s.Get(ctx, 1)
	require.NoError(t, err)
	assert.False(t, page.Enabled)
	// The branch and directory are kept for next time.
	assert.Equal(t, "main", page.Branch)
	assert.Equal(t, "/", page.Dir)
}
