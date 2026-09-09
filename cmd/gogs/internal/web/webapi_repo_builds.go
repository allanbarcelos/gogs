package web

import (
	"context"
	"net/http"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/flamego/flamego"
	"github.com/gogs/git-module"
	log "unknwon.dev/clog/v2"

	"gogs.io/gogs/internal/database"
	"gogs.io/gogs/internal/repox"
)

type buildStatus struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	Context     string    `json:"context"`
	Description string    `json:"description"`
	TargetURL   string    `json:"targetURL"`
	Creator     string    `json:"creator,omitempty"`
	Created     time.Time `json:"created"`
}

type buildGroup struct {
	SHA      string         `json:"sha"`
	State    string         `json:"state"`
	Statuses []*buildStatus `json:"statuses"`
}

type repoBuilds struct {
	Enabled bool          `json:"enabled"`
	Groups  []*buildGroup `json:"groups"`
}

type repoCommitStatuses struct {
	SHA      string         `json:"sha"`
	State    string         `json:"state"`
	Latest   []*buildStatus `json:"latest"`
	Attempts []*buildStatus `json:"attempts"`
}

// creatorNameResolver caches user name lookups by ID for the lifetime of one
// request.
type creatorNameResolver struct {
	ctx   context.Context
	cache map[int64]string
}

func newCreatorNameResolver(ctx context.Context) *creatorNameResolver {
	return &creatorNameResolver{ctx: ctx, cache: make(map[int64]string)}
}

func (r *creatorNameResolver) name(id int64) string {
	if name, ok := r.cache[id]; ok {
		return name
	}
	name := ""
	if u, err := database.Handle.Users().GetByID(r.ctx, id); err == nil && u != nil {
		name = u.Name
	}
	r.cache[id] = name
	return name
}

func toBuildStatus(s *database.CommitStatus, creator string) *buildStatus {
	return &buildStatus{
		ID:          s.ID,
		State:       string(s.State),
		Context:     s.Context,
		Description: s.Description,
		TargetURL:   s.TargetURL,
		Creator:     creator,
		Created:     s.Created.UTC(),
	}
}

// latestPerContext reduces rows (which must be newest-first) to the most recent
// row per context, preserving that order.
func latestPerContext(rows []*database.CommitStatus) []*database.CommitStatus {
	seen := make(map[string]struct{}, len(rows))
	out := make([]*database.CommitStatus, 0, len(rows))
	for _, row := range rows {
		if _, ok := seen[row.Context]; ok {
			continue
		}
		seen[row.Context] = struct{}{}
		out = append(out, row)
	}
	return out
}

func combinedState(rows []*database.CommitStatus) string {
	states := make([]database.CommitStatusState, len(rows))
	for i, row := range rows {
		states[i] = row.State
	}
	return string(database.CombineCommitStatusStates(states...))
}

// getRepoBuilds returns recent commit statuses for the repository, grouped by
// commit, newest first.
//
// GET /api/web/{owner}/{repo}/builds
func getRepoBuilds(c flamego.Context, repoCtx *repoContext) (int, *repoBuilds, error) {
	if !repoCtx.ViewerCanRead() {
		return http.StatusNotFound, nil, errors.New("repository does not exist")
	}

	repo := repoCtx.Repo
	if !repo.EnableCommitStatus {
		return http.StatusOK, &repoBuilds{Enabled: false, Groups: []*buildGroup{}}, nil
	}

	ctx := c.Request().Context()
	rows, err := database.Handle.CommitStatuses().ListByRepo(ctx, repo.ID, 200)
	if err != nil {
		log.Error("getRepoBuilds: list statuses for repo %d: %v", repo.ID, err)
		return http.StatusInternalServerError, nil, errors.Wrap(err, "list statuses")
	}

	resolver := newCreatorNameResolver(ctx)
	groups := make([]*buildGroup, 0)
	index := make(map[string]*buildGroup)
	perGroupRows := make(map[string][]*database.CommitStatus)

	for _, row := range rows {
		g, ok := index[row.CommitSHA]
		if !ok {
			g = &buildGroup{SHA: row.CommitSHA}
			index[row.CommitSHA] = g
			groups = append(groups, g)
		}
		perGroupRows[row.CommitSHA] = append(perGroupRows[row.CommitSHA], row)
	}

	for _, g := range groups {
		latest := latestPerContext(perGroupRows[g.SHA])
		g.State = combinedState(latest)
		for _, row := range latest {
			g.Statuses = append(g.Statuses, toBuildStatus(row, resolver.name(row.CreatorID)))
		}
	}

	return http.StatusOK, &repoBuilds{Enabled: true, Groups: groups}, nil
}

// getRepoCommitStatuses returns every status for one commit: the latest per
// context plus the full attempt history.
//
// GET /api/web/{owner}/{repo}/commit/{sha}/statuses
func getRepoCommitStatuses(c flamego.Context, repoCtx *repoContext) (int, *repoCommitStatuses, error) {
	if !repoCtx.ViewerCanRead() {
		return http.StatusNotFound, nil, errors.New("repository does not exist")
	}

	repo := repoCtx.Repo
	if !repo.EnableCommitStatus {
		return http.StatusNotFound, nil, errors.New("builds are disabled for this repository")
	}

	ctx := c.Request().Context()
	commitID := c.Param("sha")
	if gitRepo, err := git.Open(repox.RepositoryPath(repoCtx.Owner.Name, repo.Name)); err == nil {
		if resolved, err := gitRepo.RevParse(commitID); err == nil {
			commitID = resolved
		}
	}

	rows, err := database.Handle.CommitStatuses().List(ctx, repo.ID, commitID)
	if err != nil {
		log.Error("getRepoCommitStatuses: list statuses for repo %d commit %s: %v", repo.ID, commitID, err)
		return http.StatusInternalServerError, nil, errors.Wrap(err, "list statuses")
	}

	resolver := newCreatorNameResolver(ctx)
	latest := latestPerContext(rows)

	resp := &repoCommitStatuses{
		SHA:      commitID,
		State:    combinedState(latest),
		Latest:   make([]*buildStatus, 0, len(latest)),
		Attempts: make([]*buildStatus, 0, len(rows)),
	}
	for _, row := range latest {
		resp.Latest = append(resp.Latest, toBuildStatus(row, resolver.name(row.CreatorID)))
	}
	for _, row := range rows {
		resp.Attempts = append(resp.Attempts, toBuildStatus(row, resolver.name(row.CreatorID)))
	}

	return http.StatusOK, resp, nil
}
