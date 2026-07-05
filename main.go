package main

import (
	_ "embed"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/os/gctx"

	"duckdb-api/internal/api"
	"duckdb-api/internal/duck"
	"duckdb-api/internal/store"
)

//go:embed web/index.html
var indexHTML []byte

func main() {
	ctx := gctx.GetInitCtx()

	dataDir := envStr("DATA_DIR", "./data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		g.Log().Fatalf(ctx, "create data dir %s: %v", dataDir, err)
	}

	st, err := store.New(filepath.Join(dataDir, "tables.json"))
	if err != nil {
		g.Log().Fatalf(ctx, "load table store: %v", err)
	}

	svc, err := duck.New(ctx, st, duck.Config{
		DataDir:      dataDir,
		AllowWrite:   envBool("ALLOW_WRITE", false),
		MaxRows:      envInt("MAX_ROWS", 10000),
		QueryTimeout: time.Duration(envInt("QUERY_TIMEOUT", 300)) * time.Second,
	})
	if err != nil {
		g.Log().Fatalf(ctx, "open duckdb: %v", err)
	}
	defer svc.Close()

	if err := svc.ExportMetabase(ctx); err != nil {
		g.Log().Warningf(ctx, "export metabase catalog: %v", err)
	}

	apiKey := envStr("API_KEY", "")
	if apiKey == "" {
		g.Log().Warning(ctx, "API_KEY is empty: the API is unauthenticated, do not expose it publicly")
	}

	localDir := envStr("LOCAL_DIR", "./localdata")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		g.Log().Fatalf(ctx, "create local dir %s: %v", localDir, err)
	}

	s := g.Server()
	s.SetPort(envInt("PORT", 8080))
	// Allow large data file uploads to /api/upload.
	s.SetClientMaxBodySize(2 << 30)
	api.Register(s, svc, st, indexHTML, apiKey, localDir)
	g.Log().Infof(ctx, "duckdb-api starting, data dir: %s", dataDir)
	s.Run()
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envBool(key string, def bool) bool {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
