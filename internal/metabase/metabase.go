// Package metabase notifies a Metabase instance that the exported DuckDB
// catalog changed, so new tables show up without waiting for the hourly scan
// or restarting Metabase.
package metabase

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Client struct {
	baseURL    string
	apiKey     string
	databaseID int

	mu         sync.Mutex
	resolvedID int
	http       *http.Client
}

// New returns a client, or nil when not configured (baseURL or apiKey empty).
// databaseID 0 means auto-discover: the first database with engine "duckdb".
func New(baseURL, apiKey string, databaseID int) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.TrimSpace(apiKey) == "" {
		return nil
	}
	return &Client{
		baseURL:    baseURL,
		apiKey:     apiKey,
		databaseID: databaseID,
		http:       &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) do(ctx context.Context, method, path string, payload any) ([]byte, error) {
	var reqBody io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		reqBody = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("metabase %s %s: HTTP %d: %.200s", method, path, resp.StatusCode, body)
	}
	return body, nil
}

// resolveDatabaseID finds the DuckDB database registered in Metabase.
func (c *Client) resolveDatabaseID(ctx context.Context) (int, error) {
	if c.databaseID > 0 {
		return c.databaseID, nil
	}
	c.mu.Lock()
	cached := c.resolvedID
	c.mu.Unlock()
	if cached > 0 {
		return cached, nil
	}

	body, err := c.do(ctx, http.MethodGet, "/api/database", nil)
	if err != nil {
		return 0, err
	}
	// Newer Metabase returns {"data":[...]}, older returns [...].
	var wrapped struct {
		Data []struct {
			ID     int    `json:"id"`
			Engine string `json:"engine"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil || len(wrapped.Data) == 0 {
		if err2 := json.Unmarshal(body, &wrapped.Data); err2 != nil {
			return 0, fmt.Errorf("parse metabase database list: %v", err)
		}
	}
	for _, db := range wrapped.Data {
		if db.Engine == "duckdb" {
			c.mu.Lock()
			c.resolvedID = db.ID
			c.mu.Unlock()
			return db.ID, nil
		}
	}
	return 0, fmt.Errorf("no duckdb database found in metabase")
}

// SyncSchema asks Metabase to re-scan the DuckDB catalog now.
func (c *Client) SyncSchema(ctx context.Context) error {
	id, err := c.resolveDatabaseID(ctx)
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/api/database/%d/sync_schema", id), nil)
	return err
}

// Refresh points Metabase at a freshly-exported catalog copy and triggers a
// schema sync. Metabase pools long-lived DuckDB connections and its driver
// caches database instances per file path, so a plain sync — or even flipping
// between two fixed paths — still reads stale metadata. A never-seen-before
// versioned path guarantees a fresh instance.
func (c *Client) Refresh(ctx context.Context, catalogPath string) error {
	if catalogPath == "" {
		return c.SyncSchema(ctx)
	}
	id, err := c.resolveDatabaseID(ctx)
	if err != nil {
		return err
	}
	body, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/database/%d", id), nil)
	if err != nil {
		return err
	}
	var db struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal(body, &db); err != nil || db.Details == nil {
		return fmt.Errorf("parse metabase database %d details: %v", id, err)
	}
	if current, _ := db.Details["database_file"].(string); current == catalogPath {
		return c.SyncSchema(ctx)
	}
	db.Details["database_file"] = catalogPath
	if _, err := c.do(ctx, http.MethodPut, fmt.Sprintf("/api/database/%d", id), map[string]any{"details": db.Details}); err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/api/database/%d/sync_schema", id), nil)
	return err
}
