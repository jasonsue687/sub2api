package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type anthropicCaptureRepo struct {
	service.OpsRepository
	filter *service.AnthropicRequestFilter
}

func (r *anthropicCaptureRepo) ListAnthropicRequests(_ context.Context, f *service.AnthropicRequestFilter) (*service.AnthropicRequestList, error) {
	r.filter = f
	return &service.AnthropicRequestList{}, nil
}
func TestAnthropicHandlerScopeValidation(t *testing.T) {
	for _, tt := range []struct {
		query  string
		status int
	}{
		{"", 400}, {"account_id=0", 400}, {"account_id=invalid", 400}, {"account_id=11&time_range=30d", 400}, {"account_id=11&only_mismatch=invalid", 400},
		{"account_id=11&start_time=2026-09-01T00:00:00Z&end_time=2026-09-09T00:00:00Z", 400},
		{"account_id=11&start_time=2026-09-01T00:00:00Z&end_time=2026-09-01T00:00:00Z", 400},
		{"account_id=11&only_mismatch=true&page_size=500", 200},
	} {
		t.Run(tt.query, func(t *testing.T) {
			repo := &anthropicCaptureRepo{}
			svc := service.NewOpsService(repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			h := &OpsHandler{opsService: svc}
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.GET("/requests", h.ListAnthropicRequests)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/requests?"+tt.query, nil))
			if w.Code != tt.status {
				t.Fatalf("got %d: %s", w.Code, w.Body.String())
			}
			if tt.status == 200 && (repo.filter.AccountID != 11 || !repo.filter.OnlyMismatch || repo.filter.PageSize != 100) {
				t.Fatalf("bad filter %+v", repo.filter)
			}
			if tt.status != 200 && repo.filter != nil {
				t.Fatal("invalid request reached repository")
			}
		})
	}
}
