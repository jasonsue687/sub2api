package anthropicmock

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCorpusDedupeAndSecretRedaction(t *testing.T) {
	ResetForTest()
	t.Cleanup(ResetForTest)
	mem := NewMemoryStore()
	SetStore(mem)
	body := `{"model":"claude","api_key":"sk-ant-abcdefghijklmnopqrstuvwxyz","messages":[{"role":"user","content":"hello"}]}`
	first := mustBodyReq(t, "/v1/messages", body)
	ObserveInbound(first)
	restored, _ := io.ReadAll(first.Body)
	if string(restored) != body {
		t.Fatalf("handler body changed: %s", restored)
	}
	if !strings.Contains(first.Header.Get("Authorization"), "Bearer") {
		t.Fatal("authorization header should stay on the live request")
	}
	processInbound(<-inboundQ)
	again := mustBodyReq(t, "/v1/messages", body)
	ObserveInbound(again)
	processInbound(<-inboundQ)
	otherClient := mustBodyReq(t, "/v1/messages", body)
	otherClient.Header.Set("User-Agent", "cursor/1.0")
	ObserveInbound(otherClient)
	processInbound(<-inboundQ)
	n, err := mem.CountSamples(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("samples=%d err=%v", n, err)
	}
	items, _, err := mem.ListSamples(context.Background(), 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listed %d", len(items))
	}
	for _, item := range items {
		if strings.Contains(string(item.Body), "sk-ant-") || strings.Contains(string(item.Body), "abcdefghijklmnopqrstuvwxyz") {
			t.Fatalf("secret persisted: %s", item.Body)
		}
		if _, ok := item.Headers["Authorization"]; ok {
			t.Fatal("authorization header stored")
		}
	}
}

func TestOutboundHeadersRedactCredentials(t *testing.T) {
	req := mustBodyReq(t, "/v1/messages", `{"model":"claude","access_token":"super-secret-token"}`)
	req.Header.Set("X-Api-Key", "sk-live")
	headers := redactHeaders(req)
	if headers["Authorization"] != "[redacted]" || headers["X-Api-Key"] != "[redacted]" {
		t.Fatalf("headers %+v", headers)
	}
	if strings.Contains(headers["User-Agent"], "sk-") {
		t.Fatal(headers["User-Agent"])
	}
	body := RedactBody("application/json", []byte(`{"model":"claude","access_token":"super-secret-token"}`))
	if strings.Contains(string(body), "super-secret-token") {
		t.Fatalf("body %s", body)
	}
}
