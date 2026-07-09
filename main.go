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
	"duckdb-api/internal/metabase"
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
	mustBeWritable(ctx, dataDir)

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
	if err := writableProbe(localDir); err != nil {
		g.Log().Warningf(ctx, "local dir %s is not writable by uid %d — uploads and local tables will fail until you chown it (e.g. chown -R %d:%d %s): %v",
			localDir, os.Getuid(), os.Getuid(), os.Getgid(), localDir, err)
	}

	mb := metabase.New(envStr("METABASE_URL", ""), envStr("METABASE_API_KEY", ""), envInt("METABASE_DATABASE_ID", 0))
	if mb == nil {
		g.Log().Info(ctx, "metabase auto-sync disabled (set METABASE_URL and METABASE_API_KEY to enable)")
	}

	s := g.Server()
	s.SetPort(envInt("PORT", 8080))
	// Allow large data file uploads to /api/upload.
	s.SetClientMaxBodySize(2 << 30)
	api.Register(s, svc, st, indexHTML, apiKey, localDir, mb)
	g.Log().Infof(ctx, "duckdb-api starting, data dir: %s", dataDir)
	s.Run()
}

func writableProbe(dir string) error {
	probe := filepath.Join(dir, ".rw-probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		return err
	}
	return os.Remove(probe)
}

// mustBeWritable fails fast with an actionable message when the data dir is
// still owned by another uid (e.g. a volume created by an older root-based
// image, or a root-owned bind mount).
func mustBeWritable(ctx context.Context, dir string) {
	if err := writableProbe(dir); err == nil {
		return
	}
	g.Log().Fatalf(ctx,
		"data dir %s is not writable by uid %d (this container runs as a non-root user).\n"+
			"Fix ownership once, then restart:\n"+
			"  named volume:  docker run --rm -v <volume>:/d alpine chown -R %d:%d /d\n"+
			"  bind mount:    chown -R %d:%d <host dir>\n"+
			"See README 「非 root 运行」.",
		dir, os.Getuid(), os.Getuid(), os.Getgid(), os.Getuid(), os.Getgid())
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
