package anthropicmock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// MemoryStore is an in-process Store used by tests and as a stand-in when
// describing replay without a database.
type MemoryStore struct {
	mu       sync.Mutex
	setting  map[string]string
	samples  []Sample
	outbound []Outbound
	nextID   int64
}

// NewMemoryStore returns an empty memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{setting: map[string]string{}}
}

func (m *MemoryStore) GetSetting(_ context.Context, key string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.setting[key]
	return v, ok, nil
}

func (m *MemoryStore) SetSetting(_ context.Context, key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.setting == nil {
		m.setting = map[string]string{}
	}
	m.setting[key] = value
	return nil
}

func (m *MemoryStore) InsertSample(ctx context.Context, sample Sample) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if sample.DedupeKey == "" {
		sample.DedupeKey = DedupeKey(sample.Method, sample.Path, sample.ClientLabel, sample.ContentType, sample.Body)
	}
	if sample.BodySHA256 == "" {
		sum := sha256.Sum256(sample.Body)
		sample.BodySHA256 = hex.EncodeToString(sum[:])
	}
	if sample.CreatedAt.IsZero() {
		sample.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.samples {
		if existing.DedupeKey == sample.DedupeKey {
			return false, nil
		}
	}
	m.nextID++
	sample.ID = m.nextID
	sample.Body = append([]byte(nil), sample.Body...)
	sample.Headers = copyHeaders(sample.Headers)
	m.samples = append(m.samples, sample)
	return true, nil
}

func (m *MemoryStore) ListSamples(ctx context.Context, offset, limit int) ([]Sample, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	total := int64(len(m.samples))
	if offset < 0 {
		offset = 0
	}
	if offset >= len(m.samples) || limit <= 0 {
		return []Sample{}, total, nil
	}
	end := offset + limit
	if end > len(m.samples) {
		end = len(m.samples)
	}
	// Newest first.
	items := make([]Sample, 0, end-offset)
	for i := len(m.samples) - 1 - offset; i >= len(m.samples)-end; i-- {
		if i < 0 {
			break
		}
		items = append(items, cloneSample(m.samples[i]))
	}
	return items, total, nil
}

func (m *MemoryStore) CountSamples(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return int64(len(m.samples)), nil
}

func (m *MemoryStore) InsertOutbound(ctx context.Context, item Outbound) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	item.ID = m.nextID
	item.Body = append([]byte(nil), item.Body...)
	item.Headers = copyHeaders(item.Headers)
	m.outbound = append(m.outbound, item)
	return nil
}

func (m *MemoryStore) ListOutbound(ctx context.Context, offset, limit int) ([]Outbound, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	total := int64(len(m.outbound))
	if offset < 0 {
		offset = 0
	}
	if offset >= len(m.outbound) || limit <= 0 {
		return []Outbound{}, total, nil
	}
	end := offset + limit
	if end > len(m.outbound) {
		end = len(m.outbound)
	}
	items := make([]Outbound, 0, end-offset)
	for i := len(m.outbound) - 1 - offset; i >= len(m.outbound)-end; i-- {
		if i < 0 {
			break
		}
		items = append(items, m.outbound[i])
	}
	return items, total, nil
}

func cloneSample(s Sample) Sample {
	s.Body = append([]byte(nil), s.Body...)
	s.Headers = copyHeaders(s.Headers)
	return s
}

func copyHeaders(h map[string]string) map[string]string {
	if h == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}
