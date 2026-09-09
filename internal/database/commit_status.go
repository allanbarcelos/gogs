package database

import (
	"context"
	"fmt"
	"time"

	"github.com/cockroachdb/errors"
	"gorm.io/gorm"

	"gogs.io/gogs/internal/errx"
)

// CommitStatusState is the state of a CI check reported for a commit.
type CommitStatusState string

const (
	CommitStatusPending CommitStatusState = "pending"
	CommitStatusRunning CommitStatusState = "running"
	CommitStatusSuccess CommitStatusState = "success"
	CommitStatusFailure CommitStatusState = "failure"
	CommitStatusError   CommitStatusState = "error"
)

// IsValid returns true if the state is one of the recognized values.
func (s CommitStatusState) IsValid() bool {
	switch s {
	case CommitStatusPending, CommitStatusRunning, CommitStatusSuccess, CommitStatusFailure, CommitStatusError:
		return true
	}
	return false
}

// rank returns the precedence of the state within a combined status. The higher
// rank wins, i.e., it is the one reported as the combined state.
func (s CommitStatusState) rank() int {
	switch s {
	case CommitStatusError, CommitStatusFailure:
		return 3
	case CommitStatusRunning:
		return 2
	case CommitStatusPending:
		return 1
	case CommitStatusSuccess:
		return 0
	}
	return -1
}

// CombineCommitStatusStates reduces the given states to a single combined state
// using worst-wins precedence: "error" or "failure" beats "running", "running"
// beats "pending", "pending" beats "success". It returns an empty string when
// no states are given.
func CombineCommitStatusStates(states ...CommitStatusState) CommitStatusState {
	if len(states) == 0 {
		return ""
	}
	worst := states[0]
	for _, s := range states[1:] {
		if s.rank() > worst.rank() {
			worst = s
		}
	}
	return worst
}

// CommitStatus is a single CI check outcome reported for a commit in a
// repository. Rows are append only. The current status for a context is the
// most recently created row carrying that context.
type CommitStatus struct {
	ID          int64             `gorm:"primaryKey"`
	RepoID      int64             `gorm:"index:commit_status_repo_commit;index:commit_status_repo_commit_context;not null"`
	CommitSHA   string            `gorm:"column:commit_sha;index:commit_status_repo_commit;index:commit_status_repo_commit_context;type:VARCHAR(40);not null"`
	State       CommitStatusState `gorm:"type:VARCHAR(20);not null"`
	Context     string            `gorm:"index:commit_status_repo_commit_context;type:VARCHAR(191);not null"`
	TargetURL   string
	Description string
	CreatorID   int64 `gorm:"not null"`
	CreatedUnix int64
	UpdatedUnix int64

	Created time.Time `gorm:"-" json:"-"`
	Updated time.Time `gorm:"-" json:"-"`
}

// BeforeCreate implements the GORM create hook.
func (s *CommitStatus) BeforeCreate(tx *gorm.DB) error {
	if s.CreatedUnix == 0 {
		s.CreatedUnix = tx.NowFunc().Unix()
	}
	if s.UpdatedUnix == 0 {
		s.UpdatedUnix = s.CreatedUnix
	}
	return nil
}

// AfterFind implements the GORM query hook.
func (s *CommitStatus) AfterFind(_ *gorm.DB) error {
	s.Created = time.Unix(s.CreatedUnix, 0).Local()
	s.Updated = time.Unix(s.UpdatedUnix, 0).Local()
	return nil
}

// CommitStatusesStore is the storage layer for commit statuses.
type CommitStatusesStore struct {
	db *gorm.DB
}

func newCommitStatusesStore(db *gorm.DB) *CommitStatusesStore {
	return &CommitStatusesStore{db: db}
}

// DefaultCommitStatusContext is used when a caller reports a status without a
// context, matching the behavior of the GitHub status API.
const DefaultCommitStatusContext = "default"

type ErrTooManyCommitStatusContexts struct {
	args errx.Args
}

func IsErrTooManyCommitStatusContexts(err error) bool {
	return errors.As(err, &ErrTooManyCommitStatusContexts{})
}

func (err ErrTooManyCommitStatusContexts) Error() string {
	return fmt.Sprintf("too many commit status contexts: %v", err.args)
}

type CreateCommitStatusOptions struct {
	RepoID      int64
	CreatorID   int64
	CommitSHA   string
	State       CommitStatusState
	Context     string
	TargetURL   string
	Description string
	// MaxContexts caps the number of distinct contexts a single commit may
	// carry. A value of zero disables the check. When the limit is reached, a
	// status for an already-seen context still succeeds, but one for a new
	// context returns ErrTooManyCommitStatusContexts.
	MaxContexts int
}

// Create appends a new commit status row and returns it.
func (s *CommitStatusesStore) Create(ctx context.Context, opts CreateCommitStatusOptions) (*CommitStatus, error) {
	statusContext := opts.Context
	if statusContext == "" {
		statusContext = DefaultCommitStatusContext
	}

	status := &CommitStatus{
		RepoID:      opts.RepoID,
		CreatorID:   opts.CreatorID,
		CommitSHA:   opts.CommitSHA,
		State:       opts.State,
		Context:     statusContext,
		TargetURL:   opts.TargetURL,
		Description: opts.Description,
	}

	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if opts.MaxContexts > 0 {
			var contexts []string
			err := tx.Model(new(CommitStatus)).
				Where("repo_id = ? AND commit_sha = ?", opts.RepoID, opts.CommitSHA).
				Distinct().
				Pluck("context", &contexts).Error
			if err != nil {
				return errors.Wrap(err, "list distinct contexts")
			}

			seen := false
			for _, c := range contexts {
				if c == statusContext {
					seen = true
					break
				}
			}
			if !seen && len(contexts) >= opts.MaxContexts {
				return ErrTooManyCommitStatusContexts{
					args: errx.Args{
						"repoID":    opts.RepoID,
						"commitSHA": opts.CommitSHA,
						"max":       opts.MaxContexts,
					},
				}
			}
		}

		return tx.Create(status).Error
	})
	if err != nil {
		return nil, err
	}
	return status, nil
}

// List returns every status row for the given commit, newest first.
func (s *CommitStatusesStore) List(ctx context.Context, repoID int64, commitSHA string) ([]*CommitStatus, error) {
	var statuses []*CommitStatus
	return statuses, s.db.WithContext(ctx).
		Where("repo_id = ? AND commit_sha = ?", repoID, commitSHA).
		Order("id DESC").
		Find(&statuses).Error
}

// Latest returns the most recent status per context for the given commit,
// newest first.
func (s *CommitStatusesStore) Latest(ctx context.Context, repoID int64, commitSHA string) ([]*CommitStatus, error) {
	all, err := s.List(ctx, repoID, commitSHA)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(all))
	latest := make([]*CommitStatus, 0, len(all))
	for _, st := range all {
		if _, ok := seen[st.Context]; ok {
			continue
		}
		seen[st.Context] = struct{}{}
		latest = append(latest, st)
	}
	return latest, nil
}

// CombinedState returns the combined state of the latest status per context for
// the given commit. It returns an empty string when the commit has no statuses.
func (s *CommitStatusesStore) CombinedState(ctx context.Context, repoID int64, commitSHA string) (CommitStatusState, error) {
	latest, err := s.Latest(ctx, repoID, commitSHA)
	if err != nil {
		return "", err
	}

	states := make([]CommitStatusState, len(latest))
	for i, st := range latest {
		states[i] = st.State
	}
	return CombineCommitStatusStates(states...), nil
}
