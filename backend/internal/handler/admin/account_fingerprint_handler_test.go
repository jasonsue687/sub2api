package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fingerprintBindingHandlerStore struct {
	service.AccountFingerprintStore
	accountID     int64
	fingerprintID *int64
	calls         int
}

func (s *fingerprintBindingHandlerStore) Bind(_ context.Context, accountID int64, fingerprintID *int64) error {
	s.accountID, s.fingerprintID = accountID, fingerprintID
	s.calls++
	return nil
}

func TestFingerprintBindingHandlerRequiresExplicitSelectionOrUnbind(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		body   string
		status int
		id     int64
	}{
		{`{}`, 400, 0}, {`{"fingerprint_id":0}`, 400, 0}, {`{"fingerprint_id":-1}`, 400, 0},
		{`{"fingerprint_id":"7"}`, 400, 0}, {`{"fingerprint_id":true}`, 400, 0},
		{`{"fingerprint_id":null}`, 200, 0}, {`{"fingerprint_id":7}`, 200, 7},
	} {
		t.Run(tc.body, func(t *testing.T) {
			store := &fingerprintBindingHandlerStore{}
			router := gin.New()
			router.PUT("/accounts/:id/fingerprint-binding", NewAccountFingerprintHandler(store).Bind)
			req := httptest.NewRequest(http.MethodPut, "/accounts/14/fingerprint-binding", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status != 200 {
				assert.Zero(t, store.calls)
				return
			}
			assert.Equal(t, int64(14), store.accountID)
			if tc.id == 0 {
				assert.Nil(t, store.fingerprintID)
			} else {
				require.NotNil(t, store.fingerprintID)
				assert.Equal(t, tc.id, *store.fingerprintID)
			}
			assert.Contains(t, w.Body.String(), `"applied":false`)
		})
	}
}
