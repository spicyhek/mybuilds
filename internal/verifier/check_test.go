package verifier

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

const release = "personal-site/0123456/production/57"

// server answers /healthz the way a configured response says.
type response struct {
	status  int
	release string
	body    string
	delay   time.Duration
}

func newServer(t *testing.T, r *response) (*httptest.Server, Checker) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(r.delay)
		if r.release != "" {
			w.Header().Set("X-Release-ID", r.release)
		}
		w.Header().Set("cf-ray", "a5997cd8ffd46770-LAX")
		w.Header().Set("cf-cache-status", "DYNAMIC")
		w.WriteHeader(r.status)
		w.Write([]byte(r.body))
	}))
	t.Cleanup(srv.Close)
	base := srv.Client().Transport.(*http.Transport)
	return srv, Checker{Transport: func() *http.Transport {
		tr := base.Clone()
		tr.DisableKeepAlives = true
		return tr
	}}
}

func TestCheckResults(t *testing.T) {
	health := &services.HealthBody{Field: "status", Value: "ok"}
	cases := []struct {
		name   string
		resp   response
		health *services.HealthBody
		maxMS  int
		result string
		reason string
	}{
		{"pass", response{status: 200, release: release, body: "ok\n"}, nil, 0, events.CheckPass, ""},
		{"old replica", response{status: 200, release: "personal-site/fffffff/production/56"}, nil, 0,
			events.CheckMismatch, "served personal-site/fffffff/production/56"},
		{"no header", response{status: 200}, nil, 0, events.CheckMismatch, "served no X-Release-ID"},
		{"server error", response{status: 500, release: release}, nil, 0, events.CheckFail, "HTTP 500"},
		{"slow", response{status: 200, release: release, delay: 150 * time.Millisecond}, nil, 50,
			events.CheckFail, "over the 50 ms limit"},
		{"healthy body", response{status: 200, release: release, body: `{"status":"ok","warnings":["x"]}`},
			health, 0, events.CheckPass, ""},
		{"degraded body", response{status: 200, release: release, body: `{"status":"degraded"}`},
			health, 0, events.CheckUnhealthy, `status is not "ok"`},
		{"unparsable body", response{status: 200, release: release, body: "ok\n"},
			health, 0, events.CheckUnhealthy, "health body"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, c := newServer(t, &tc.resp)
			v := services.Verify{URL: srv.URL + "/healthz", MaxLatencyMS: tc.maxMS, HealthBody: tc.health}
			res := c.Check(context.Background(), v, release, time.Now())
			if res.Result != tc.result || !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("result %s %q, want %s containing %q", res.Result, res.Reason, tc.result, tc.reason)
			}
			if !res.DNSOK || !res.TLSOK || res.TLSVersion == "" || res.CertNotAfter == nil {
				t.Errorf("connection fields %+v", res)
			}
			if res.Colo != "LAX" || res.StatusCode != tc.resp.status {
				t.Errorf("colo %q status %d", res.Colo, res.StatusCode)
			}
			if (tc.health != nil) != (res.HealthOK != nil) {
				t.Errorf("health_ok %v with health check %v", res.HealthOK, tc.health)
			}
		})
	}
}

func TestCheckConnectionFailures(t *testing.T) {
	srv, _ := newServer(t, &response{status: 200, release: release})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "https://" + ln.Addr().String() + "/healthz"
	ln.Close()

	cases := []struct {
		name, url, reason string
		dnsOK             bool
	}{
		// The default transport does not trust the test server's certificate.
		{"untrusted certificate", srv.URL + "/healthz", "TLS handshake failed", true},
		{"nothing listening", closed, "Connection failed", true},
		{"no such host", "https://mybuilds-verifier-test.invalid/healthz", "DNS lookup failed", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Checker{}.Check(context.Background(), services.Verify{URL: tc.url}, release, time.Now())
			if res.Result != events.CheckFail || res.Reason != tc.reason || res.DNSOK != tc.dnsOK {
				t.Errorf("got %s %q dns_ok=%t, want fail %q dns_ok=%t", res.Result, res.Reason, res.DNSOK, tc.reason, tc.dnsOK)
			}
		})
	}
}

func TestRedirectIsNotFollowed(t *testing.T) {
	srv := httptest.NewTLSServer(http.RedirectHandler("https://elsewhere.test/", http.StatusFound))
	defer srv.Close()
	base := srv.Client().Transport.(*http.Transport)
	c := Checker{Transport: func() *http.Transport { return base.Clone() }}
	res := c.Check(context.Background(), services.Verify{URL: srv.URL + "/healthz"}, release, time.Now())
	if res.Result != events.CheckFail || res.StatusCode != http.StatusFound {
		t.Errorf("redirect: %s %d %q", res.Result, res.StatusCode, res.Reason)
	}
}
