package anthropicmock

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRunRefusesWithoutHookAndDoesNotReplay(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	gin.SetMode(gin.TestMode)
	called := 0
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		called++
		c.Status(http.StatusOK)
	})
	SetReplayHandler(engine)
	SetStore(NewMemoryStore())
	_, err := Run(context.Background(), RunOptions{})
	if !errors.Is(err, ErrMockNotGuaranteed) {
		t.Fatalf("err=%v", err)
	}
	if called != 0 {
		t.Fatal("replay started without a guarantee")
	}
	if testRuns.Load() != 0 {
		t.Fatal("test guard leaked")
	}
}

func TestRunRefusesWhenInterceptCannotBeProven(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	gin.SetMode(gin.TestMode)
	called := 0
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		called++
		c.Status(http.StatusOK)
	})
	ArmOutboundHook()
	SetReplayHandler(engine)
	mem := NewMemoryStore()
	_, _ = mem.InsertSample(context.Background(), Sample{Method: http.MethodPost, Path: "/v1/messages", Body: []byte(`{"model":"claude"}`)})
	SetStore(mem)
	DisableInterceptForTest(true)
	_, err := Run(context.Background(), RunOptions{Limit: 1})
	if !errors.Is(err, ErrMockNotGuaranteed) {
		t.Fatalf("err=%v", err)
	}
	if called != 0 {
		t.Fatal("samples were replayed after the proof failed")
	}
	if testRuns.Load() != 0 {
		t.Fatal("test guard leaked after refusal")
	}
}

func TestRunForcesMockEvenWhenSwitchIsOff(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	gin.SetMode(gin.TestMode)
	upstreamCalls := 0
	engine := gin.New()
	engine.POST("/v1/messages", func(c *gin.Context) {
		// Drop the inbound context. The test-run guard must still mock Anthropic.
		req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", c.Request.Body)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		rt := WrapRoundTripper(RoundTripFunc(func(*http.Request) (*http.Response, error) {
			upstreamCalls++
			return nil, context.Canceled
		}))
		resp, err := rt.RoundTrip(req)
		if err != nil {
			c.Status(http.StatusBadGateway)
			return
		}
		defer func() { _ = resp.Body.Close() }()
		c.Status(resp.StatusCode)
	})
	ArmOutboundHook()
	SetReplayHandler(engine)
	SetEnabledForTest(false)
	mem := NewMemoryStore()
	_, _ = mem.InsertSample(context.Background(), Sample{
		Method: http.MethodPost, Path: "/v1/messages", ContentType: "application/json",
		Body: []byte(`{"model":"claude-sonnet-4-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`),
	})
	SetStore(mem)
	report, err := Run(context.Background(), RunOptions{Limit: 1, APIKey: "sk-test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream calls=%d", upstreamCalls)
	}
	if report.Total != 1 || report.Completed != 1 || !report.MockGuaranteed {
		t.Fatalf("%+v", report)
	}
	if Enabled() {
		t.Fatal("one-click run enabled the global switch")
	}
}
