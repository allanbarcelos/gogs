package v1

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"gogs.io/gogs/internal/database"
)

func TestIsAcceptableTargetURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		want bool
	}{
		{in: "", want: true},
		{in: "https://ci.example.com/job/1", want: true},
		{in: "http://10.0.0.1:8080/build/42", want: true},
		{in: "HTTPS://CI.EXAMPLE.COM/x", want: true},
		{in: "javascript:alert(1)", want: false},
		{in: "data:text/html,<script>", want: false},
		{in: "ftp://example.com/x", want: false},
		{in: "/relative/path", want: false},
		{in: "ci.example.com/no-scheme", want: false},
		{in: "https://ci.example.com/" + strings.Repeat("a", 2100), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, isAcceptableTargetURL(tc.in))
		})
	}
}

func TestParseCommitStatusState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    string
		want  database.CommitStatusState
		valid bool
	}{
		{in: "pending", want: database.CommitStatusPending, valid: true},
		{in: "running", want: database.CommitStatusRunning, valid: true},
		{in: "success", want: database.CommitStatusSuccess, valid: true},
		{in: "failure", want: database.CommitStatusFailure, valid: true},
		{in: "error", want: database.CommitStatusError, valid: true},
		{in: "", valid: false},
		{in: "Success", valid: false},
		{in: "queued", valid: false},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got, ok := parseCommitStatusState(tc.in)
			assert.Equal(t, tc.valid, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestToCommitStatus(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	status := &database.CommitStatus{
		ID:          7,
		State:       database.CommitStatusSuccess,
		TargetURL:   "https://ci.example.com/7",
		Description: "Passed",
		Context:     "jenkins/build",
		Created:     now,
		Updated:     now,
	}

	got := toCommitStatus(status, nil)
	assert.Equal(t, int64(7), got.ID)
	assert.Equal(t, "success", got.State)
	assert.Equal(t, "https://ci.example.com/7", got.TargetURL)
	assert.Equal(t, "Passed", got.Description)
	assert.Equal(t, "jenkins/build", got.Context)
	assert.Equal(t, now, got.Created)
	assert.Equal(t, now, got.Updated)
	assert.Nil(t, got.Creator)
}
