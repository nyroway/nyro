// Package protocoltest is the test-only harness for nyro's protocol-conversion
// matrix: it drives the real proxy router for a given inbound×outbound protocol
// pair, feeds the upstream from a recorded cassette, and asserts the two
// translation directions (client→upstream request, upstream→client response)
// against golden files.
//
// It is imported only by *_test.go files, so it never enters a production
// binary. See docs/superpowers/specs/2026-07-14-go-protocol-testing-strategy-design.md.
package protocoltest

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Cassette is the transparent record/replay artifact for one upstream
// interaction. Unlike an opaque VCR YAML blob, the response body is stored as
// readable text (raw JSON or raw SSE) so a human can review it before commit.
// Only non-sensitive request metadata is stored (method + path) — never request
// headers — so a committed cassette can never leak an API key.
type Cassette struct {
	// Note flags placeholder cassettes that must be re-recorded with real keys.
	Note     string           `json:"note,omitempty"`
	Request  CassetteRequest  `json:"request"`
	Response CassetteResponse `json:"response"`
}

// CassetteRequest is the recorded upstream request metadata (no headers). Model
// is the resolved upstream target model at record time; replay reuses it so the
// request-direction golden is identical across record/replay regardless of
// which real model was used to record.
type CassetteRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Model  string `json:"model,omitempty"`
}

// CassetteResponse is the recorded upstream response. Body is verbatim provider
// bytes (JSON for non-stream, raw SSE text for stream).
type CassetteResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"`
}

// loadCassette reads and parses a cassette file.
func loadCassette(path string) (*Cassette, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Cassette
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("parse cassette %s: %w", path, err)
	}
	return &c, nil
}

// save writes a cassette as indented JSON (stable, reviewable diffs).
func (c *Cassette) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// replayTransport is an http.RoundTripper that serves a fixed cassette response
// without any network I/O, and captures the outbound request the gateway
// produced (so the harness can assert the request-translation direction).
type replayTransport struct {
	cas *Cassette

	mu      sync.Mutex
	gotBody []byte
	gotPath string
	gotVerb string
}

func (rt *replayTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
	}
	rt.mu.Lock()
	rt.gotBody = body
	rt.gotPath = req.URL.Path
	rt.gotVerb = req.Method
	rt.mu.Unlock()

	header := make(http.Header)
	for k, v := range rt.cas.Response.Headers {
		header.Set(k, v)
	}
	return &http.Response{
		StatusCode: rt.cas.Response.Status,
		Status:     http.StatusText(rt.cas.Response.Status),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(rt.cas.Response.Body)),
		Request:    req,
	}, nil
}

// outboundRequest returns the captured request the gateway sent upstream.
func (rt *replayTransport) outboundRequest() (verb, path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.gotVerb, rt.gotPath, rt.gotBody
}

// recordTransport wraps a real RoundTripper: it captures the outbound request
// (for the request-direction golden), forwards it to the live provider, tees
// the response into a cassette (response body + non-sensitive request metadata
// only — never request headers), and returns the response unchanged.
type recordTransport struct {
	real http.RoundTripper
	path string // cassette file to write
	note string

	mu      sync.Mutex
	gotBody []byte
	gotPath string
	gotVerb string
}

func (rt *recordTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var reqBody []byte
	if req.Body != nil {
		reqBody, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(reqBody))
	}
	rt.mu.Lock()
	rt.gotBody, rt.gotPath, rt.gotVerb = reqBody, req.URL.Path, req.Method
	rt.mu.Unlock()

	resp, err := rt.real.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, err
	}
	// Preserve only content-type; drop everything else (may carry rate-limit /
	// set-cookie / request-id noise, and we never want request headers here).
	headers := map[string]string{}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		headers["Content-Type"] = ct
	}
	cas := &Cassette{
		Note:     rt.note,
		Request:  CassetteRequest{Method: req.Method, Path: req.URL.Path, Model: jsonModel(reqBody)},
		Response: CassetteResponse{Status: resp.StatusCode, Headers: headers, Body: string(raw)},
	}
	if err := cas.save(rt.path); err != nil {
		return nil, fmt.Errorf("save cassette: %w", err)
	}
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	return resp, nil
}

func (rt *recordTransport) outboundRequest() (verb, path string, body []byte) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.gotVerb, rt.gotPath, rt.gotBody
}

// capturingTransport is the UpstreamTransport contract the harness relies on:
// an http.RoundTripper that also exposes the outbound request the gateway
// produced, so both record and replay paths can assert the request direction.
type capturingTransport interface {
	http.RoundTripper
	outboundRequest() (verb, path string, body []byte)
}

// jsonModel extracts the top-level "model" field from a JSON request body
// (empty if absent / not JSON). Used to pin the recorded target model.
func jsonModel(body []byte) string {
	var m struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &m)
	return m.Model
}

// sseFrames splits a raw SSE body into its data payloads (the text after
// "data: "), dropping the "[DONE]" terminator. Used by the stream direction to
// normalize each event's JSON independently.
func sseFrames(body string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		out = append(out, payload)
	}
	return out
}
