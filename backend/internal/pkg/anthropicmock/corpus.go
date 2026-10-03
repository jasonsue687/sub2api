package anthropicmock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type inboundJob struct {
	Method      string
	Path        string
	ContentType string
	ClientLabel string
	Headers     map[string]string
	Body        []byte
}

type outboundRecord struct {
	AccountID     int64
	Method        string
	URL           string
	Headers       map[string]string
	Body          []byte
	BodyTruncated bool
	MockReason    string
	StatusCode    int
	CreatedAt     time.Time
}

// Start applies the config/env default, then keeps the persisted admin switch
// fresh and drains the capture queues. It is safe to call once at process start.
func Start(st Store, configEnabled bool) {
	SetStore(st)
	enabled.Store(resolveInitial(configEnabled))
	source.Store("config")
	startOnce.Do(func() {
		go refreshSettings()
		go runInbound()
		go runOutbound()
	})
}

func resolveInitial(configEnabled bool) bool {
	if v, ok := envBool("SUB2API_ANTHROPIC_MOCK_ENABLED"); ok {
		return v
	}
	if v, ok := envBool("GATEWAY_ANTHROPIC_MOCK_ENABLED"); ok {
		return v
	}
	return configEnabled
}

func envBool(key string) (bool, bool) {
	raw, ok := os.LookupEnv(key)
	if !ok {
		return false, false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return false, false
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return v, true
}

func refreshSettings() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		refreshOnce()
		<-ticker.C
	}
}

func refreshOnce() {
	st := currentStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	val, ok, err := st.GetSetting(ctx, settingKey)
	if err != nil || !ok {
		return
	}
	enabled.Store(val == "true")
	source.Store("db")
}

func runInbound() {
	for job := range inboundQ {
		func() {
			defer func() { _ = recover() }()
			processInbound(job)
		}()
	}
}

func runOutbound() {
	for rec := range outboundQ {
		func() {
			defer func() { _ = recover() }()
			processOutbound(rec)
		}()
	}
}

func processInbound(job inboundJob) {
	body := RedactBody(job.ContentType, job.Body)
	key := DedupeKey(job.Method, job.Path, job.ClientLabel, job.ContentType, body)
	if _, ok := seen.Load(key); ok {
		return
	}
	sum := sha256.Sum256(body)
	sample := Sample{
		CreatedAt:   time.Now().UTC(),
		DedupeKey:   key,
		Method:      job.Method,
		Path:        job.Path,
		ClientLabel: job.ClientLabel,
		ContentType: job.ContentType,
		Headers:     job.Headers,
		Body:        body,
		BodySHA256:  hex.EncodeToString(sum[:]),
	}
	st := currentStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := st.InsertSample(ctx, sample); err != nil {
		n := inboundFailed.Add(1)
		if n == 1 || n%100 == 0 {
			slog.Warn("anthropic mock sample insert failed", "error", err, "failures", n)
		}
		return
	}
	remember(key)
}

func processOutbound(rec outboundRecord) {
	st := currentStore()
	if st == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	item := Outbound{
		CreatedAt:     rec.CreatedAt,
		AccountID:     rec.AccountID,
		Method:        rec.Method,
		URL:           rec.URL,
		Headers:       rec.Headers,
		Body:          rec.Body,
		BodyTruncated: rec.BodyTruncated,
		MockReason:    rec.MockReason,
		StatusCode:    rec.StatusCode,
	}
	if err := st.InsertOutbound(ctx, item); err != nil {
		n := outboundFailed.Add(1)
		if n == 1 || n%100 == 0 {
			slog.Warn("anthropic mock outbound insert failed", "error", err, "failures", n)
		}
	}
}

func remember(key string) {
	if seenN.Load() >= maxSeenKeys {
		return
	}
	if _, loaded := seen.LoadOrStore(key, struct{}{}); !loaded {
		seenN.Add(1)
	}
}

// DedupeKey identifies a corpus sample by client, entry, and redacted body.
// The same body sent by a different client or to a different path is kept.
func DedupeKey(method, path, clientLabel, contentType string, body []byte) string {
	h := sha256.New()
	_, _ = io.WriteString(h, strings.ToUpper(strings.TrimSpace(method)))
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, path)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, clientLabel)
	_, _ = h.Write([]byte{0})
	_, _ = io.WriteString(h, contentType)
	_, _ = h.Write([]byte{0})
	sum := sha256.Sum256(body)
	_, _ = h.Write(sum[:])
	return hex.EncodeToString(h.Sum(nil))
}

// ObserveInbound copies a gateway request body onto a bounded queue.
// The queue never blocks the caller; overflow is counted and dropped.
func ObserveInbound(r *http.Request) {
	if r == nil || r.URL == nil || skipCapture(r.Context()) {
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return
	}
	if !capturePath(r.URL.Path) {
		return
	}
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/") {
		return
	}
	if r.ContentLength > maxInboundBody {
		return
	}
	if len(inboundQ) >= cap(inboundQ) {
		inboundDropped.Add(1)
		return
	}
	body, ok := takeBody(r)
	if !ok {
		return
	}
	job := inboundJob{
		Method:      r.Method,
		Path:        r.URL.Path,
		ContentType: mediaType(r.Header.Get("Content-Type")),
		ClientLabel: clientLabel(r.Header.Get("User-Agent")),
		Headers:     allowInboundHeaders(r.Header),
		Body:        body,
	}
	select {
	case inboundQ <- job:
	default:
		inboundDropped.Add(1)
	}
}

func takeBody(r *http.Request) ([]byte, bool) {
	if r.Body == nil {
		return nil, true
	}
	original := r.Body
	buf, err := io.ReadAll(io.LimitReader(original, maxInboundBody+1))
	if err != nil {
		r.Body = &replayBody{r: io.MultiReader(bytesReader(buf), original), c: original}
		return nil, false
	}
	if len(buf) > maxInboundBody {
		r.Body = &replayBody{r: io.MultiReader(bytesReader(buf), original), c: original}
		return nil, false
	}
	owned := append([]byte(nil), buf...)
	_ = original.Close()
	r.Body = io.NopCloser(bytesReader(owned))
	r.ContentLength = int64(len(owned))
	r.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytesReader(owned)), nil
	}
	return append([]byte(nil), owned...), true
}

type replayBody struct {
	r io.Reader
	c io.Closer
}

func (b *replayBody) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b *replayBody) Close() error {
	if b.c == nil {
		return nil
	}
	return b.c.Close()
}

func bytesReader(b []byte) io.Reader { return &sliceReader{b: b} }

type sliceReader struct{ b []byte }

func (s *sliceReader) Read(p []byte) (int, error) {
	if len(s.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, s.b)
	s.b = s.b[n:]
	return n, nil
}

func capturePath(path string) bool {
	switch {
	case path == "/v1" || strings.HasPrefix(path, "/v1/"):
		return true
	case strings.HasPrefix(path, "/v1beta/"):
		return true
	case strings.HasPrefix(path, "/v3/") || strings.HasPrefix(path, "/api/v3/"):
		return true
	case strings.HasPrefix(path, "/antigravity/"):
		return true
	case strings.HasPrefix(path, "/backend-api/"):
		return true
	case path == "/responses" || strings.HasPrefix(path, "/responses/"):
		return true
	case path == "/chat/completions" || strings.HasPrefix(path, "/chat/"):
		return true
	case path == "/messages" || strings.HasPrefix(path, "/messages/"):
		return true
	case path == "/embeddings" || strings.HasPrefix(path, "/embeddings"):
		return true
	case strings.HasPrefix(path, "/images/") || strings.HasPrefix(path, "/videos/"):
		return true
	case path == "/alpha/search" || strings.HasPrefix(path, "/alpha/"):
		return true
	case path == "/web_search" || path == "/x_search":
		return true
	case strings.HasPrefix(path, "/contents/"):
		return true
	default:
		return false
	}
}

func mediaType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(ct))
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

func clientLabel(ua string) string {
	ua = strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, ua))
	lower := strings.ToLower(ua)
	if strings.Contains(lower, "sk-") || strings.Contains(lower, "bearer ") {
		sum := sha256.Sum256([]byte(ua))
		return "ua-" + hex.EncodeToString(sum[:8])
	}
	if ua == "" {
		return "unknown"
	}
	return bound(ua, 180)
}
