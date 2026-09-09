package types

import "time"

// CommitStatus is a single CI check outcome reported for a commit.
type CommitStatus struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	TargetURL   string    `json:"target_url"`
	Description string    `json:"description"`
	Context     string    `json:"context"`
	Creator     *User     `json:"creator"`
	Created     time.Time `json:"created_at"`
	Updated     time.Time `json:"updated_at"`
}

// CombinedStatus is the aggregate of the latest status per context for a
// commit.
type CombinedStatus struct {
	State      string          `json:"state"`
	SHA        string          `json:"sha"`
	TotalCount int             `json:"total_count"`
	Statuses   []*CommitStatus `json:"statuses"`
}

// CreateStatusOption is the payload for reporting a commit status.
type CreateStatusOption struct {
	State       string `json:"state" binding:"Required"`
	TargetURL   string `json:"target_url"`
	Description string `json:"description"`
	Context     string `json:"context"`
}
