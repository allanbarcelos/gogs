package database

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCommitStatusState(t *testing.T) {
	t.Parallel()

	assert.True(t, CommitStatusPending.IsValid())
	assert.True(t, CommitStatusRunning.IsValid())
	assert.True(t, CommitStatusSuccess.IsValid())
	assert.True(t, CommitStatusFailure.IsValid())
	assert.True(t, CommitStatusError.IsValid())
	assert.False(t, CommitStatusState("").IsValid())
	assert.False(t, CommitStatusState("bogus").IsValid())
}

func TestCombineCommitStatusStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		states []CommitStatusState
		want   CommitStatusState
	}{
		{name: "empty", states: nil, want: ""},
		{name: "single success", states: []CommitStatusState{CommitStatusSuccess}, want: CommitStatusSuccess},
		{name: "all success", states: []CommitStatusState{CommitStatusSuccess, CommitStatusSuccess}, want: CommitStatusSuccess},
		{name: "pending beats success", states: []CommitStatusState{CommitStatusSuccess, CommitStatusPending}, want: CommitStatusPending},
		{name: "running beats pending", states: []CommitStatusState{CommitStatusPending, CommitStatusRunning, CommitStatusSuccess}, want: CommitStatusRunning},
		{name: "failure beats running", states: []CommitStatusState{CommitStatusRunning, CommitStatusFailure}, want: CommitStatusFailure},
		{name: "error beats running", states: []CommitStatusState{CommitStatusRunning, CommitStatusError}, want: CommitStatusError},
		{name: "failure and error equal, first kept", states: []CommitStatusState{CommitStatusFailure, CommitStatusError}, want: CommitStatusFailure},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CombineCommitStatusStates(tc.states...))
		})
	}
}

func TestCommitStatuses(t *testing.T) {
	if testing.Short() {
		t.Skip()
	}
	t.Parallel()

	ctx := context.Background()
	s := &CommitStatusesStore{
		db: newTestDB(t, "CommitStatusesStore"),
	}

	for _, tc := range []struct {
		name string
		test func(t *testing.T, ctx context.Context, s *CommitStatusesStore)
	}{
		{"Create", commitStatusesCreate},
		{"CreateDefaultContext", commitStatusesCreateDefaultContext},
		{"CreateMaxContexts", commitStatusesCreateMaxContexts},
		{"List", commitStatusesList},
		{"Latest", commitStatusesLatest},
		{"CombinedState", commitStatusesCombinedState},
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

func commitStatusesCreate(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	got, err := s.Create(ctx, CreateCommitStatusOptions{
		RepoID:      1,
		CreatorID:   2,
		CommitSHA:   "a1b2c3",
		State:       CommitStatusSuccess,
		Context:     "jenkins/build",
		TargetURL:   "https://ci.example.com/1",
		Description: "Passed",
	})
	require.NoError(t, err)

	assert.NotZero(t, got.ID)
	assert.Equal(t, int64(1), got.RepoID)
	assert.Equal(t, int64(2), got.CreatorID)
	assert.Equal(t, "a1b2c3", got.CommitSHA)
	assert.Equal(t, CommitStatusSuccess, got.State)
	assert.Equal(t, "jenkins/build", got.Context)
	assert.Equal(t, s.db.NowFunc().Unix(), got.CreatedUnix)
	assert.Equal(t, got.CreatedUnix, got.UpdatedUnix)
}

func commitStatusesCreateDefaultContext(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	got, err := s.Create(ctx, CreateCommitStatusOptions{
		RepoID:    1,
		CreatorID: 2,
		CommitSHA: "a1b2c3",
		State:     CommitStatusPending,
	})
	require.NoError(t, err)
	assert.Equal(t, DefaultCommitStatusContext, got.Context)
}

func commitStatusesCreateMaxContexts(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	base := CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusPending, MaxContexts: 2}

	first := base
	first.Context = "ctx-1"
	_, err := s.Create(ctx, first)
	require.NoError(t, err)

	second := base
	second.Context = "ctx-2"
	_, err = s.Create(ctx, second)
	require.NoError(t, err)

	// A new context beyond the cap is rejected.
	third := base
	third.Context = "ctx-3"
	_, err = s.Create(ctx, third)
	require.Error(t, err)
	assert.True(t, IsErrTooManyCommitStatusContexts(err))

	// An already-seen context still succeeds at the cap.
	repeat := base
	repeat.Context = "ctx-1"
	repeat.State = CommitStatusSuccess
	_, err = s.Create(ctx, repeat)
	require.NoError(t, err)
}

func commitStatusesList(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	for _, st := range []CommitStatusState{CommitStatusPending, CommitStatusRunning, CommitStatusSuccess} {
		_, err := s.Create(ctx, CreateCommitStatusOptions{
			RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: st, Context: "jenkins/build",
		})
		require.NoError(t, err)
	}
	// A status on another commit must not leak in.
	_, err := s.Create(ctx, CreateCommitStatusOptions{
		RepoID: 1, CreatorID: 2, CommitSHA: "deadbeef", State: CommitStatusFailure, Context: "jenkins/build",
	})
	require.NoError(t, err)

	got, err := s.List(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Newest first.
	assert.Equal(t, CommitStatusSuccess, got[0].State)
	assert.Equal(t, CommitStatusRunning, got[1].State)
	assert.Equal(t, CommitStatusPending, got[2].State)
}

func commitStatusesLatest(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	steps := []struct {
		state   CommitStatusState
		context string
	}{
		{CommitStatusPending, "jenkins/build"},
		{CommitStatusPending, "jenkins/e2e"},
		{CommitStatusRunning, "jenkins/build"},
		{CommitStatusSuccess, "jenkins/build"},
		{CommitStatusFailure, "jenkins/e2e"},
	}
	for _, step := range steps {
		_, err := s.Create(ctx, CreateCommitStatusOptions{
			RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: step.state, Context: step.context,
		})
		require.NoError(t, err)
	}

	got, err := s.Latest(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	require.Len(t, got, 2)

	byContext := map[string]CommitStatusState{}
	for _, st := range got {
		byContext[st.Context] = st.State
	}
	assert.Equal(t, CommitStatusSuccess, byContext["jenkins/build"])
	assert.Equal(t, CommitStatusFailure, byContext["jenkins/e2e"])
}

func commitStatusesCombinedState(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	// No statuses yet.
	combined, err := s.CombinedState(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	assert.Equal(t, CommitStatusState(""), combined)

	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusRunning, Context: "jenkins/e2e"})
	require.NoError(t, err)

	combined, err = s.CombinedState(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	assert.Equal(t, CommitStatusRunning, combined)

	// A newer failing status on an existing context flips the combined state.
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusFailure, Context: "jenkins/build"})
	require.NoError(t, err)

	combined, err = s.CombinedState(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	assert.Equal(t, CommitStatusFailure, combined)
}
