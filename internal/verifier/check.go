// Package verifier is the Verify stage's public check (plan section 8.5):
// after a release's Rollout succeeds, it requests the service's health URL
// through its public route every 30 seconds and stores each result as an
// event.
package verifier

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"slices"
	"strings"
	"time"

	"github.com/spicyhek/mybuilds/internal/events"
	"github.com/spicyhek/mybuilds/internal/services"
)

// CheckTimeout bounds one check, connection included.
const CheckTimeout = 10 * time.Second

// maxBody is how much of a response is read; health bodies are small.
const maxBody = 64 << 10

// Checker runs one check.
type Checker struct {
	// Transport returns a fresh transport per check, so every check
	// resolves DNS and completes a TLS handshake. Nil uses the default.
	Transport func() *http.Transport
	UserAgent string
}

func defaultTransport() *http.Transport {
	return &http.Transport{
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 5 * time.Second,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
}

// Check requests v.URL once and judges the response against releaseID.
func (c Checker) Check(ctx context.Context, v services.Verify, releaseID string, at time.Time) events.VerifyCheck {
	res := events.VerifyCheck{ReleaseID: releaseID, Endpoint: v.URL, CheckedAt: at.UTC(), DNSOK: true}

	ctx, cancel := context.WithTimeout(ctx, CheckTimeout)
	defer cancel()

	var dnsErr, tlsErr error
	trace := &httptrace.ClientTrace{
		DNSDone: func(i httptrace.DNSDoneInfo) { dnsErr = i.Err },
		TLSHandshakeDone: func(st tls.ConnectionState, err error) {
			tlsErr = err
			if err == nil {
				res.TLSOK = true
				res.TLSVersion = tls.VersionName(st.Version)
				if len(st.PeerCertificates) > 0 {
					t := st.PeerCertificates[0].NotAfter.UTC()
					res.CertNotAfter = &t
				}
			}
		},
	}
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), http.MethodGet, v.URL, nil)
	if err != nil {
		return fail(res, "Invalid URL")
	}
	ua := c.UserAgent
	if ua == "" {
		ua = "mybuilds-verifier"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Cache-Control", "no-cache")

	newTransport := c.Transport
	if newTransport == nil {
		newTransport = defaultTransport
	}
	tr := newTransport()
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		// A redirect is the answer: /healthz should not move.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		switch {
		case dnsErr != nil:
			res.DNSOK = false
			return fail(res, "DNS lookup failed")
		case tlsErr != nil:
			return fail(res, "TLS handshake failed")
		case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
			return fail(res, fmt.Sprintf("No response within %s", CheckTimeout))
		}
		return fail(res, "Connection failed")
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	res.LatencyMS = time.Since(start).Milliseconds()
	if resp.TLS != nil && !res.TLSOK {
		// The response carries the handshake state too, in case the trace
		// callback did not fire.
		res.TLSOK = true
		res.TLSVersion = tls.VersionName(resp.TLS.Version)
	}

	res.StatusCode = resp.StatusCode
	res.ObservedReleaseID = resp.Header.Get("X-Release-ID")
	res.CacheStatus = resp.Header.Get("cf-cache-status")
	if ray := resp.Header.Get("cf-ray"); ray != "" {
		if i := strings.LastIndex(ray, "-"); i >= 0 {
			res.Colo = ray[i+1:]
		}
	}
	if v.HealthBody != nil {
		ok := readErr == nil && healthy(body, *v.HealthBody)
		res.HealthOK = &ok
	}

	switch {
	case readErr != nil:
		return fail(res, "Response body could not be read")
	case !slices.Contains(v.Status(), resp.StatusCode):
		return fail(res, fmt.Sprintf("HTTP %d is not an allowed status", resp.StatusCode))
	case res.ObservedReleaseID != releaseID:
		res.Result = events.CheckMismatch
		res.Reason = "release mismatch: served " + orDefault(res.ObservedReleaseID, "no X-Release-ID")
	case time.Duration(res.LatencyMS)*time.Millisecond > v.MaxLatency():
		return fail(res, fmt.Sprintf("Latency %d ms is over the %d ms limit", res.LatencyMS, v.MaxLatency().Milliseconds()))
	case res.HealthOK != nil && !*res.HealthOK:
		res.Result = events.CheckUnhealthy
		res.Reason = fmt.Sprintf("health body %s is not %q", v.HealthBody.Field, v.HealthBody.Value)
	default:
		res.Result = events.CheckPass
	}
	return res
}

func fail(res events.VerifyCheck, reason string) events.VerifyCheck {
	res.Result, res.Reason = events.CheckFail, reason
	return res
}

// healthy reports whether the JSON body's top-level field equals want.
func healthy(body []byte, h services.HealthBody) bool {
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		return false
	}
	got, ok := doc[h.Field].(string)
	return ok && got == h.Value
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
