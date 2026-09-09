package database

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
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

func TestVerifyCommitStatusSignature(t *testing.T) {
	t.Parallel()

	secret := "s3cr3t"
	body := []byte(`{"state":"success"}`)

	assert.False(t, VerifyCommitStatusSignature("", body, "sha256=deadbeef"))
	assert.False(t, VerifyCommitStatusSignature(secret, body, ""))
	assert.False(t, VerifyCommitStatusSignature(secret, body, "sha256=deadbeef"))

	// A signature produced with the real helper must round-trip, with and
	// without the "sha256=" prefix.
	sig := hmacHex(t, secret, body)
	assert.True(t, VerifyCommitStatusSignature(secret, body, sig))
	assert.True(t, VerifyCommitStatusSignature(secret, body, "sha256="+sig))
	assert.False(t, VerifyCommitStatusSignature(secret, []byte("tampered"), sig))
}

func hmacHex(t *testing.T, secret string, body []byte) string {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	_, err := mac.Write(body)
	require.NoError(t, err)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestEncryptCommitStatusSecret(t *testing.T) {
	plain := "abcdefghijklmnopqrstuvwxyz0123456789ABCD"
	stored, err := encryptCommitStatusSecret(plain)
	require.NoError(t, err)
	assert.NotEqual(t, plain, stored)
	got, err := decryptCommitStatusSecret(stored)
	require.NoError(t, err)
	assert.Equal(t, plain, got)

	legacy, err := decryptCommitStatusSecret(plain)
	require.NoError(t, err)
	assert.Equal(t, plain, legacy)
}

func TestParseStatusContexts(t *testing.T) {
	assert.Equal(t, []string{"jenkins/build", "jenkins/e2e"}, ParseStatusContexts("jenkins/build\njenkins/e2e\njenkins/build"))
	assert.Equal(t, []string{"a", "b"}, ParseStatusContexts("a, b , ,a"))
	assert.Empty(t, ParseStatusContexts("  \n  "))
}

func TestGenerateCommitStatusSecret(t *testing.T) {
	t.Parallel()

	a, err := GenerateCommitStatusSecret()
	require.NoError(t, err)
	b, err := GenerateCommitStatusSecret()
	require.NoError(t, err)

	assert.Len(t, a, 40)
	assert.NotEqual(t, a, b)
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
		{"CreateWithCreatorName", commitStatusesCreateWithCreatorName},
		{"CreateDefaultContext", commitStatusesCreateDefaultContext},
		{"CreateMaxContexts", commitStatusesCreateMaxContexts},
		{"CreateMaxContextsConcurrent", commitStatusesCreateMaxContextsConcurrent},
		{"UnmetRequiredStatusChecks", commitStatusesUnmetRequired},
		{"ReportAndReadRoundTrip", commitStatusesReportAndReadRoundTrip},
		{"List", commitStatusesList},
		{"ListByRepo", commitStatusesListByRepo},
		{"ListByRecentCommits", commitStatusesListByRecentCommits},
		{"DeleteByRepo", commitStatusesDeleteByRepo},
		{"Latest", commitStatusesLatest},
		{"CombinedState", commitStatusesCombinedState},
		{"DeleteBefore", commitStatusesDeleteBefore},
		{"PruneContextAttempts", commitStatusesPruneContextAttempts},
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

func commitStatusesCreateWithCreatorName(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	got, err := s.Create(ctx, CreateCommitStatusOptions{
		RepoID:      1,
		CreatorName: "ci",
		CommitSHA:   "a1b2c3",
		State:       CommitStatusSuccess,
		Context:     "jenkins/build",
	})
	require.NoError(t, err)
	assert.Zero(t, got.CreatorID)
	assert.Equal(t, "ci", got.CreatorName)

	round, err := s.List(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	require.Len(t, round, 1)
	assert.Equal(t, "ci", round[0].CreatorName)
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

func commitStatusesCreateMaxContextsConcurrent(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	const max = 2
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = s.Create(ctx, CreateCommitStatusOptions{
				RepoID: 1, CreatorID: 2, CommitSHA: "race", State: CommitStatusPending,
				Context: fmt.Sprintf("ctx-%d", i), MaxContexts: max,
			})
		}(i)
	}
	wg.Wait()

	latest, err := s.Latest(ctx, 1, "race")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(latest), max)
}

func commitStatusesUnmetRequired(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	unmet, err := s.UnmetRequiredStatusChecks(ctx, 1, "", []string{"jenkins/build"})
	require.NoError(t, err)
	assert.Equal(t, []string{"jenkins/build"}, unmet)

	unmet, err = s.UnmetRequiredStatusChecks(ctx, 1, "abc", []string{"jenkins/build"})
	require.NoError(t, err)
	assert.Equal(t, []string{"jenkins/build"}, unmet)

	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "abc", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)
	unmet, err = s.UnmetRequiredStatusChecks(ctx, 1, "abc", []string{"jenkins/build", "jenkins/e2e"})
	require.NoError(t, err)
	assert.Equal(t, []string{"jenkins/e2e"}, unmet)
}

func commitStatusesReportAndReadRoundTrip(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	repo := &Repository{ID: 7, EnableCommitStatus: true}
	require.NoError(t, ensureCommitStatusSecret(repo))
	plain := repo.PlainCommitStatusSecret()
	require.Len(t, plain, 40)

	body := []byte(`{"state":"success","context":"jenkins/build"}`)
	mac := hmac.New(sha256.New, []byte(plain))
	_, err := mac.Write(body)
	require.NoError(t, err)
	sig := hex.EncodeToString(mac.Sum(nil))
	require.True(t, VerifyCommitStatusSignature(plain, body, "sha256="+sig))

	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 7, CreatorName: "ci", CommitSHA: "deadbeef", State: CommitStatusPending, Context: "jenkins/build"})
	require.NoError(t, err)
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 7, CreatorName: "ci", CommitSHA: "deadbeef", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)

	combined, err := s.CombinedState(ctx, 7, "deadbeef")
	require.NoError(t, err)
	assert.Equal(t, CommitStatusSuccess, combined)
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

func commitStatusesListByRepo(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	for _, sha := range []string{"aaa", "bbb", "ccc"} {
		_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: sha, State: CommitStatusSuccess, Context: "jenkins/build"})
		require.NoError(t, err)
	}
	// Another repository must not leak in.
	_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 2, CreatorID: 2, CommitSHA: "zzz", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)

	got, err := s.ListByRepo(ctx, 1, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
	// Newest first.
	assert.Equal(t, "ccc", got[0].CommitSHA)

	limited, err := s.ListByRepo(ctx, 1, 2)
	require.NoError(t, err)
	assert.Len(t, limited, 2)
}

func commitStatusesListByRecentCommits(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	// One quiet commit, then a busy commit with many attempts, then another quiet one.
	_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "quiet-old", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "busy", State: CommitStatusPending, Context: "jenkins/build"})
		require.NoError(t, err)
	}
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "quiet-new", State: CommitStatusFailure, Context: "jenkins/e2e"})
	require.NoError(t, err)
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 2, CreatorID: 2, CommitSHA: "other-repo", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)

	shas, rows, err := s.ListByRecentCommits(ctx, 1, 2)
	require.NoError(t, err)
	require.Equal(t, []string{"quiet-new", "busy"}, shas)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		assert.Contains(t, []string{"quiet-new", "busy"}, row.CommitSHA)
	}

	emptySHAs, emptyRows, err := s.ListByRecentCommits(ctx, 99, 10)
	require.NoError(t, err)
	assert.Empty(t, emptySHAs)
	assert.Empty(t, emptyRows)
}

func commitStatusesDeleteByRepo(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "aaa", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)
	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 2, CreatorID: 2, CommitSHA: "bbb", State: CommitStatusSuccess, Context: "jenkins/build"})
	require.NoError(t, err)

	err = s.DeleteByRepo(ctx, 1)
	require.NoError(t, err)

	left, err := s.ListByRepo(ctx, 1, 100)
	require.NoError(t, err)
	assert.Empty(t, left)
	kept, err := s.ListByRepo(ctx, 2, 100)
	require.NoError(t, err)
	require.Len(t, kept, 1)
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

func commitStatusesDeleteBefore(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	old, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusSuccess, Context: "old"})
	require.NoError(t, err)
	err = s.db.WithContext(ctx).Model(new(CommitStatus)).Where("id = ?", old.ID).Update("created_unix", 1000).Error
	require.NoError(t, err)

	_, err = s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusSuccess, Context: "new"})
	require.NoError(t, err)

	removed, err := s.DeleteBefore(ctx, 2000)
	require.NoError(t, err)
	assert.Equal(t, int64(1), removed)

	remaining, err := s.List(ctx, 1, "a1b2c3")
	require.NoError(t, err)
	require.Len(t, remaining, 1)
	assert.Equal(t, "new", remaining[0].Context)
}

func commitStatusesPruneContextAttempts(t *testing.T, ctx context.Context, s *CommitStatusesStore) {
	for i := 0; i < 5; i++ {
		_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusPending, Context: "jenkins/build"})
		require.NoError(t, err)
	}
	for i := 0; i < 2; i++ {
		_, err := s.Create(ctx, CreateCommitStatusOptions{RepoID: 1, CreatorID: 2, CommitSHA: "a1b2c3", State: CommitStatusPending, Context: "jenkins/e2e"})
		require.NoError(t, err)
	}

	// keep <= 0 is a no-op.
	removed, err := s.PruneContextAttempts(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), removed)

	removed, err = s.PruneContextAttempts(ctx, 2)
	require.NoError(t, err)
	assert.Equal(t, int64(3), removed)

	all, err := s.List(ctx, 1, "a1b2c3")
	require.NoError(t, err)

	perContext := map[string]int{}
	for _, st := range all {
		perContext[st.Context]++
	}
	assert.Equal(t, 2, perContext["jenkins/build"])
	assert.Equal(t, 2, perContext["jenkins/e2e"])
}
