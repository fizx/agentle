package caps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/kylemaxwell/agentle/internal/engine"
)

// HTTPConfig is a bound http tool instance: which hosts it may reach, timeouts,
// size caps, and headers injected from secrets (never visible to the script).
type HTTPConfig struct {
	Allow        []string          // host allowlist; exact ("api.x.com") or suffix ("*.x.com")
	Timeout      time.Duration     // per-request timeout
	MaxBytes     int64             // response body cap
	Headers      map[string]string // injected on every request (e.g. Authorization from a secret)
	AllowPrivate bool              // permit private/loopback targets (dev only)
}

type httpExecutor struct {
	cfg    HTTPConfig
	client *http.Client
}

// HTTP returns the "http" capability executor enforcing cfg.
func HTTP(cfg HTTPConfig) engine.Executor {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = 5 << 20 // 5 MiB
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: guardedControl(cfg.AllowPrivate)}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          50,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: cfg.Timeout,
	}
	return &httpExecutor{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout, Transport: transport}}
}

type httpArgs struct {
	URL     string            `json:"url"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}

func (e *httpExecutor) Execute(ctx context.Context, inv engine.Invocation) (json.RawMessage, error) {
	var a httpArgs
	if err := json.Unmarshal(inv.Args, &a); err != nil {
		return nil, err
	}
	method := http.MethodGet
	if inv.Method == "post" {
		method = http.MethodPost
	}

	if err := e.checkHost(a.URL); err != nil {
		return nil, err
	}

	var body io.Reader
	if a.Body != "" {
		body = strings.NewReader(a.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.URL, body)
	if err != nil {
		return nil, err
	}
	for k, v := range a.Headers {
		req.Header.Set(k, v)
	}
	for k, v := range e.cfg.Headers { // injected secrets win over script headers
		req.Header.Set(k, v)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, e.cfg.MaxBytes))
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"status": resp.StatusCode,
		"body":   string(data),
		"headers": func() map[string]string {
			h := map[string]string{}
			for k := range resp.Header {
				h[k] = resp.Header.Get(k)
			}
			return h
		}(),
	}
	return json.Marshal(out)
}

func (e *httpExecutor) checkHost(rawURL string) error {
	u, err := parseHTTPURL(rawURL)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if !hostAllowed(host, e.cfg.Allow) {
		return fmt.Errorf("http: host %q not in allowlist", host)
	}
	return nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("http: invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("http: scheme %q not allowed", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("http: missing host")
	}
	return u, nil
}

func hostAllowed(host string, allow []string) bool {
	host = strings.ToLower(host)
	for _, pat := range allow {
		pat = strings.ToLower(strings.TrimSpace(pat))
		if pat == "" {
			continue
		}
		if pat == "*" {
			return true
		}
		if strings.HasPrefix(pat, "*.") {
			if strings.HasSuffix(host, pat[1:]) || host == pat[2:] {
				return true
			}
			continue
		}
		if host == pat {
			return true
		}
	}
	return false
}

// guardedControl rejects connections to private/loopback/link-local addresses,
// closing the DNS-rebinding TOCTOU window by validating the actual dialed IP.
func guardedControl(allowPrivate bool) func(network, address string, c syscall.RawConn) error {
	return func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("http: bad dial address %q", address)
		}
		ip := net.ParseIP(host)
		if ip == nil {
			return fmt.Errorf("http: cannot parse dial ip %q", host)
		}
		if !allowPrivate && disallowedIP(ip) {
			return fmt.Errorf("http: blocked connection to non-public address %s", ip)
		}
		return nil
	}
}

func disallowedIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast()
}
