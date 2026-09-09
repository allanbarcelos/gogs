package database

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"gorm.io/gorm"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/errx"
	"gogs.io/gogs/internal/strx"
)

// GenerateCommitStatusSecret returns a fresh random secret for a repository to
// authenticate commit status reports from an external CI system.
func GenerateCommitStatusSecret() (string, error) {
	return strx.RandomChars(40)
}

// ensureCommitStatusSecret mints a CI secret when the builds feature is on and
// the repository does not have one yet.
func ensureCommitStatusSecret(repo *Repository) error {
	if !repo.EnableCommitStatus || repo.CommitStatusSecret != "" {
		return nil
	}
	secret, err := GenerateCommitStatusSecret()
	if err != nil {
		return errors.Wrap(err, "generate commit status secret")
	}
	repo.CommitStatusSecret = secret
	return nil
}

// VerifyCommitStatusSignature reports whether "signature" is a valid
// HMAC-SHA256 of "body" keyed by "secret". The signature may carry a
// "sha256=" prefix, matching the header the outgoing webhooks use.
func VerifyCommitStatusSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	signature = strings.TrimPrefix(signature, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

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
	// CreatorID is the user who reported the status via a personal access
	// token, or 0 when it was reported with the repository CI secret.
	CreatorID int64 `gorm:"not null"`
	// CreatorName is a display label for a status reported with the CI secret
	// (e.g. "ci"). Empty when CreatorID identifies a real user.
	CreatorName string
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
	CreatorName string
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
		CreatorName: opts.CreatorName,
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
	// GORM does not run AfterFind on the returned row, so populate the display
	// timestamps the way a query would.
	status.Created = time.Unix(status.CreatedUnix, 0).Local()
	status.Updated = time.Unix(status.UpdatedUnix, 0).Local()
	return status, nil
}

// ListByRepo returns the most recent status rows for a repository across every
// commit, newest first, capped at "limit". A non-positive limit defaults to
// 100. Prefer ListByRecentCommits when deriving combined state so a busy
// commit cannot starve quieter ones.
func (s *CommitStatusesStore) ListByRepo(ctx context.Context, repoID int64, limit int) ([]*CommitStatus, error) {
	if limit <= 0 {
		limit = 100
	}
	var statuses []*CommitStatus
	return statuses, s.db.WithContext(ctx).
		Where("repo_id = ?", repoID).
		Order("id DESC").
		Limit(limit).
		Find(&statuses).Error
}

// ListByRecentCommits returns the distinct most recently updated commit SHAs
// (newest first, capped at commitLimit) and every status row for those SHAs,
// newest first. Combined state for a commit must be derived from this set, not
// from a raw row window.
func (s *CommitStatusesStore) ListByRecentCommits(ctx context.Context, repoID int64, commitLimit int) ([]string, []*CommitStatus, error) {
	if commitLimit <= 0 {
		commitLimit = 50
	}

	var shas []string
	err := s.db.WithContext(ctx).
		Model(new(CommitStatus)).
		Select("commit_sha").
		Where("repo_id = ?", repoID).
		Group("commit_sha").
		Order("MAX(id) DESC").
		Limit(commitLimit).
		Pluck("commit_sha", &shas).Error
	if err != nil {
		return nil, nil, errors.Wrap(err, "list recent commit SHAs")
	}
	if len(shas) == 0 {
		return shas, nil, nil
	}

	var statuses []*CommitStatus
	err = s.db.WithContext(ctx).
		Where("repo_id = ? AND commit_sha IN ?", repoID, shas).
		Order("id DESC").
		Find(&statuses).Error
	if err != nil {
		return nil, nil, errors.Wrap(err, "list statuses for recent commits")
	}
	return shas, statuses, nil
}

// DeleteByRepo removes every status row for the given repository.
func (s *CommitStatusesStore) DeleteByRepo(ctx context.Context, repoID int64) error {
	return s.db.WithContext(ctx).Where("repo_id = ?", repoID).Delete(new(CommitStatus)).Error
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

// DeleteBefore removes every status row created strictly before the given Unix
// time. It returns the number of rows removed.
func (s *CommitStatusesStore) DeleteBefore(ctx context.Context, unix int64) (int64, error) {
	result := s.db.WithContext(ctx).
		Where("created_unix < ?", unix).
		Delete(new(CommitStatus))
	return result.RowsAffected, result.Error
}

type commitStatusGroup struct {
	RepoID    int64
	CommitSHA string
	Context   string
}

// PruneContextAttempts keeps at most "keep" most recent rows for each
// (repository, commit, context) group and removes the rest. It returns the
// number of rows removed. A non-positive "keep" is a no-op.
func (s *CommitStatusesStore) PruneContextAttempts(ctx context.Context, keep int) (int64, error) {
	if keep <= 0 {
		return 0, nil
	}

	var groups []commitStatusGroup
	err := s.db.WithContext(ctx).
		Model(new(CommitStatus)).
		Select("repo_id", "commit_sha", "context").
		Group("repo_id, commit_sha, context").
		Having("COUNT(*) > ?", keep).
		Scan(&groups).Error
	if err != nil {
		return 0, errors.Wrap(err, "list oversized groups")
	}

	var removed int64
	for _, g := range groups {
		var threshold int64
		err := s.db.WithContext(ctx).
			Model(new(CommitStatus)).
			Where("repo_id = ? AND commit_sha = ? AND context = ?", g.RepoID, g.CommitSHA, g.Context).
			Order("id DESC").
			Offset(keep-1).
			Limit(1).
			Pluck("id", &threshold).Error
		if err != nil {
			return removed, errors.Wrap(err, "find keep threshold")
		}

		result := s.db.WithContext(ctx).
			Where("repo_id = ? AND commit_sha = ? AND context = ? AND id < ?", g.RepoID, g.CommitSHA, g.Context, threshold).
			Delete(new(CommitStatus))
		if result.Error != nil {
			return removed, errors.Wrap(result.Error, "delete old attempts")
		}
		removed += result.RowsAffected
	}
	return removed, nil
}

// CleanupCommitStatuses prunes stale commit status rows according to the
// configured retention window and per-context attempt cap. It is safe to call
// from a cron task.
func CleanupCommitStatuses() {
	ctx := context.Background()
	opts := conf.Repository.CommitStatus

	if opts.RetentionDays > 0 {
		before := time.Now().AddDate(0, 0, -opts.RetentionDays).Unix()
		removed, err := Handle.CommitStatuses().DeleteBefore(ctx, before)
		if err != nil {
			log.Error("Failed to delete commit statuses older than %d days: %v", opts.RetentionDays, err)
		} else if removed > 0 {
			log.Trace("Deleted %d commit statuses older than %d days", removed, opts.RetentionDays)
		}
	}

	if opts.MaxAttemptsPerContext > 0 {
		removed, err := Handle.CommitStatuses().PruneContextAttempts(ctx, opts.MaxAttemptsPerContext)
		if err != nil {
			log.Error("Failed to prune commit status attempts: %v", err)
		} else if removed > 0 {
			log.Trace("Pruned %d commit status attempts beyond the per-context cap", removed)
		}
	}
}
