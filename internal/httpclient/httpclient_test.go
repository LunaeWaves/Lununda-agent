package httpclient

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserAgentHasVersion(t *testing.T) {
	ua := UserAgent()
	if !strings.HasPrefix(ua, "lununda-agent/") {
		t.Errorf("UA should start with lununda-agent/, got %q", ua)
	}
}

func TestWrapSetsUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := &http.Client{Transport: Wrap(http.DefaultTransport)}
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if got != UserAgent() {
		t.Errorf("UA = %q, want %q", got, UserAgent())
	}
}

// A caller-provided User-Agent must NOT be overwritten — some auth flows
// and SDKs set their own UA that the upstream server validates.
func TestWrapPreservesCallerUA(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("User-Agent", "custom-sdk/1.0")
	c := &http.Client{Transport: Wrap(http.DefaultTransport)}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if got != "custom-sdk/1.0" {
		t.Errorf("UA overwritten: got %q, want custom-sdk/1.0", got)
	}
}

func TestNewClientBranded(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := NewClient(0)
	resp, err := c.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if got != UserAgent() {
		t.Errorf("UA = %q, want %q", got, UserAgent())
	}
}
