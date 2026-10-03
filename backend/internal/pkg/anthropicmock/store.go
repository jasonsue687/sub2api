package anthropicmock

import (
	"context"
	"time"
)

// Sample is one deduplicated inbound request retained for replay.
type Sample struct {
	ID          int64
	CreatedAt   time.Time
	DedupeKey   string
	Method      string
	Path        string
	ClientLabel string
	ContentType string
	Headers     map[string]string
	Body        []byte
	BodySHA256  string
}

// Outbound is one intercepted Anthropic request.
type Outbound struct {
	ID            int64
	CreatedAt     time.Time
	AccountID     int64
	Method        string
	URL           string
	Headers       map[string]string
	Body          []byte
	BodyTruncated bool
	MockReason    string
	StatusCode    int
}

// Store persists the mock switch, corpus, and outbound captures.
type Store interface {
	GetSetting(ctx context.Context, key string) (value string, ok bool, err error)
	SetSetting(ctx context.Context, key, value string) error
	InsertSample(ctx context.Context, sample Sample) (inserted bool, err error)
	ListSamples(ctx context.Context, offset, limit int) (items []Sample, total int64, err error)
	CountSamples(ctx context.Context) (int64, error)
	InsertOutbound(ctx context.Context, item Outbound) error
	ListOutbound(ctx context.Context, offset, limit int) (items []Outbound, total int64, err error)
}
