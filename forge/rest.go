package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseSize = 16 << 20
	requestTimeout  = time.Minute
)

// apiError is a non-2xx response of a Forge API.
type apiError struct {
	Method, Path string
	Status       int
	Message      string
}

func (e *apiError) Error() (text string) {
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Message)
}

// doJSON sends a method request to path (relative to apiURL) with header and
// in as JSON body (none when nil) and decodes a 2xx response into out (unless
// nil). client is http.DefaultClient when nil.
func doJSON(ctx context.Context, client *http.Client, apiURL, method, path string, header http.Header, in, out any) (err error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	base, err := url.Parse(apiURL)
	if err != nil {
		err = fmt.Errorf("parsing API URL: %w", err)
		return
	}
	target, err := base.Parse(strings.TrimSuffix(base.Path, "/") + path)
	if err != nil {
		err = fmt.Errorf("building URL: %w", err)
		return
	}
	var body io.Reader
	if in != nil {
		var data []byte
		if data, err = json.Marshal(in); err != nil {
			err = fmt.Errorf("encoding request: %w", err)
			return
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		err = fmt.Errorf("building request: %w", err)
		return
	}
	req.Header = header.Clone()
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		err = fmt.Errorf("%s %s: %w", method, target.Path, err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		err = fmt.Errorf("%s %s: reading response: %w", method, target.Path, err)
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		err = &apiError{Method: method, Path: target.Path, Status: resp.StatusCode, Message: apiMessage(data)}
		return
	}
	if out == nil {
		return
	}
	if err = json.Unmarshal(data, out); err != nil {
		err = fmt.Errorf("%s %s: decoding response: %w", method, target.Path, err)
	}
	return
}

// apiMessage returns the "message" of an error response, else the
// beginning of the body.
func apiMessage(data []byte) (message string) {
	var body struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &body) == nil && body.Message != "" {
		return body.Message
	}
	data = bytes.TrimSpace(data)
	if len(data) > 200 {
		data = data[:200]
	}
	return strconv.Quote(string(data))
}
