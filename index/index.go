// Copyright 2019 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package index provides a client for communicating with the module index.
package index

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// A Client is used by the worker service to communicate with the module index.
type Client struct {
	// URL of the module index
	url string

	// client used for HTTP requests. It is mutable for testing purposes.
	httpClient *http.Client
}

// New constructs a *Client using the provided rawurl, which is expected to
// be an absolute URI that can be directly passed to http.Get.
func New(rawurl string, httpClient *http.Client) (*Client, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, fmt.Errorf("url.Parse(%q): %v", rawurl, err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be https (got %s)", u.Scheme)
	}
	return &Client{url: strings.TrimRight(rawurl, "/"), httpClient: httpClient}, nil
}

func (c *Client) pollURL(since time.Time, limit int) string {
	values := url.Values{}
	values.Set("since", since.Format(time.RFC3339Nano))
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	return fmt.Sprintf("%s?%s", c.url, values.Encode())
}

// VersionInfo holds the version information returned by the module index.
type VersionInfo struct {
	Path      string
	Version   string
	Timestamp time.Time
}

func (v *VersionInfo) DebugString() string {
	return fmt.Sprintf("%s@%s@%s", v.Path, v.Version, v.Timestamp.Format(time.RFC3339Nano))
}

// GetVersions queries the index for new versions.
func (c *Client) GetVersions(ctx context.Context, since time.Time, limit int) ([]*VersionInfo, error) {
	u := c.pollURL(since, limit)

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("http.NewRequest(%q, %q, nil): %v", http.MethodGet, u, err)
	}
	req = req.WithContext(ctx)
	r, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ctxhttp.Get(ctx, nil, %q): %v", u, err)
	}
	defer r.Body.Close()

	var versions []*VersionInfo
	dec := json.NewDecoder(r.Body)

	// The module index returns a stream of JSON objects formatted with newline
	// as the delimiter.
	for dec.More() {
		var l VersionInfo
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("decoding JSON: %v", err)
		}
		versions = append(versions, &l)
	}
	return versions, nil
}
