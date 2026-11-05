package integration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

const InternalHost = "cliproxy.internal"

type Transport struct{ Bridge *Bridge }

type result struct {
	response *http.Response
	err      error
}

type streamWriter struct {
	header   http.Header
	request  *http.Request
	writer   *io.PipeWriter
	results  chan result
	status   int
	response *http.Response
	sent     bool
}

func (w *streamWriter) Header() http.Header { return w.header }
func (w *streamWriter) WriteHeader(status int) {
	if w.sent {
		return
	}
	if status >= 100 && status < 200 {
		return
	}
	w.sent = true
	w.status = status
	header := w.header.Clone()
	trailer := make(http.Header)
	for _, line := range header.Values("Trailer") {
		for _, key := range strings.Split(line, ",") {
			key = http.CanonicalHeaderKey(strings.TrimSpace(key))
			if key != "" && key != "Content-Length" && key != "Transfer-Encoding" && key != "Trailer" {
				trailer[key] = nil
				header.Del(key)
			}
		}
	}
	for key := range header {
		if strings.HasPrefix(key, http.TrailerPrefix) {
			header.Del(key)
		}
	}
	header.Del("Trailer")
	length := int64(-1)
	if value := header.Get("Content-Length"); value != "" {
		if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
			length = n
		}
	}
	if w.request.Method == http.MethodHead || status == http.StatusNoContent || status == http.StatusNotModified {
		length = 0
	}
	w.response = &http.Response{
		StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: header, Trailer: trailer, Request: w.request, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		ContentLength: length,
	}
	w.results <- result{response: w.response}
}
func (w *streamWriter) Write(data []byte) (int, error) {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
	if w.request.Method == http.MethodHead {
		return len(data), nil
	}
	if w.status == http.StatusNoContent || w.status == http.StatusNotModified {
		return 0, http.ErrBodyNotAllowed
	}
	return w.writer.Write(data)
}
func (w *streamWriter) Flush() {
	if !w.sent {
		w.WriteHeader(http.StatusOK)
	}
}

type responseBody struct {
	reader *io.PipeReader
	cancel context.CancelFunc
	once   sync.Once
}

func (b *responseBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *responseBody) Close() error {
	var err error
	b.once.Do(func() { b.cancel(); err = b.reader.Close() })
	return err
}

// RoundTrip streams an HTTP management request to the original Gin handler.
// It never opens a socket and cannot dial a URL supplied by a browser.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil || req.URL == nil {
		return nil, errors.New("internal transport requires a URL")
	}
	reject := func(reason string) (*http.Response, error) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, errors.New(reason)
	}
	if req.URL.Scheme != "http" || req.URL.Host != InternalHost || req.URL.User != nil || !canonical(req.URL) {
		return reject("internal transport refuses non-canonical or non-CPA target")
	}
	if strings.TrimSpace(req.Header.Get("Upgrade")) != "" {
		return reject("protocol upgrades are not permitted in internal management transport")
	}
	path := req.URL.Path
	allowed := boundary(path, "/v0/management") || boundary(path, "/v8/management/config/api-keys") ||
		boundary(path, "/v0/resource/plugins") || path == "/v1/models" || path == "/models"
	if !allowed {
		return reject("internal transport refuses non-management route")
	}
	if t.Bridge == nil {
		return reject("internal bridge is not configured")
	}
	if err := req.Context().Err(); err != nil {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, err
	}
	ctx, cancel := context.WithCancel(req.Context())
	internal, handler := t.Bridge.coreRequest(req.WithContext(ctx))
	if handler == nil {
		cancel()
		return reject("core handler is not ready")
	}
	reader, writer := io.Pipe()
	results := make(chan result, 1)
	done := make(chan struct{})
	sw := &streamWriter{header: make(http.Header), request: req, writer: writer, results: results}
	go func() {
		defer close(done)
		if internal.Body != nil {
			defer internal.Body.Close()
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				err := errors.New("internal management handler panicked")
				if !sw.sent {
					results <- result{err: err}
				}
				_ = writer.CloseWithError(err)
			}
		}()
		handler.ServeHTTP(sw, internal)
		if !sw.sent {
			sw.WriteHeader(http.StatusOK)
		}
		for key := range sw.response.Trailer {
			sw.response.Trailer[key] = sw.header.Values(key)
		}
		for key, values := range sw.header {
			if strings.HasPrefix(key, http.TrailerPrefix) {
				sw.response.Trailer[strings.TrimPrefix(key, http.TrailerPrefix)] = append([]string(nil), values...)
			}
		}
		_ = writer.Close()
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = writer.CloseWithError(ctx.Err())
			if internal.Body != nil {
				_ = internal.Body.Close()
			}
		case <-done:
		}
	}()
	select {
	case r := <-results:
		if r.err != nil {
			cancel()
			_ = reader.Close()
			return nil, r.err
		}
		r.response.Body = &responseBody{reader: reader, cancel: cancel}
		return r.response, nil
	case <-ctx.Done():
		cancel()
		_ = reader.Close()
		return nil, ctx.Err()
	}
}
