package duck

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	duckdb "github.com/marcboeker/go-duckdb/v2"

	"duckdb-api/internal/model"
	"duckdb-api/internal/store"
)

type Config struct {
	DataDir      string
	AllowWrite   bool
	MaxRows      int
	QueryTimeout time.Duration
}

// Service owns a shared in-memory DuckDB instance. Registered tables are
// recreated from the store on startup and kept in sync on every change.
type Service struct {
	cfg      Config
	store    *store.Store
	db       *sql.DB
	bootOnce sync.Once
	exportMu sync.Mutex
}

type Result struct {
	Columns    []string `json:"columns"`
	Types      []string `json:"types"`
	Rows       [][]any  `json:"rows"`
	RowCount   int      `json:"row_count"`
	Truncated  bool     `json:"truncated"`
	DurationMs int64    `json:"duration_ms"`
}

func New(ctx context.Context, st *store.Store, cfg Config) (*Service, error) {
	if cfg.MaxRows <= 0 {
		cfg.MaxRows = 10000
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = 5 * time.Minute
	}
	s := &Service{cfg: cfg, store: st}
	for _, dir := range []string{s.extensionDir(), s.secretDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	connector, err := duckdb.NewConnector("", s.bootstrap)
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	pingCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, err
	}
	s.db = db
	return s, nil
}

func (s *Service) Close() error {
	return s.db.Close()
}

func (s *Service) extensionDir() string { return filepath.Join(s.cfg.DataDir, "extensions") }
func (s *Service) secretDir() string    { return filepath.Join(s.cfg.DataDir, "secrets") }

// bootstrap runs when the first connection to the shared in-memory database is
// opened: it loads httpfs and recreates all registered tables. Best effort — a
// broken table must not take the whole service down.
func (s *Service) bootstrap(execer driver.ExecerContext) error {
	s.bootOnce.Do(func() {
		ctx := context.Background()
		run := func(q string) error {
			_, err := execer.ExecContext(ctx, q, nil)
			return err
		}
		for _, q := range CoreSQL(s.extensionDir(), "") {
			if err := run(q); err != nil {
				g.Log().Warningf(ctx, "duckdb bootstrap %q: %v", q, err)
			}
		}
		for _, t := range s.store.List() {
			for _, q := range TableSQL(t, false) {
				if err := run(q); err != nil {
					g.Log().Warningf(ctx, "duckdb bootstrap table %q: %v", t.Name, err)
					break
				}
			}
		}
	})
	return nil
}

// ApplyTable registers (or replaces) the secret and view for a table on the
// live database and returns the resulting schema. On failure the view is
// dropped again.
func (s *Service) ApplyTable(ctx context.Context, t model.Table) (*Result, error) {
	for _, q := range TableSQL(t, false) {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return nil, fmt.Errorf("duckdb: %w", err)
		}
	}
	schema, err := s.rawQuery(ctx, "DESCRIBE "+QuoteIdent(t.Name), nil, 10000)
	if err != nil {
		for _, q := range DropTableSQL(t.Name) {
			_, _ = s.db.ExecContext(ctx, q)
		}
		return nil, fmt.Errorf("duckdb: %w", err)
	}
	return schema, nil
}

func (s *Service) RemoveTable(ctx context.Context, name string) error {
	var firstErr error
	for _, q := range DropTableSQL(name) {
		if _, err := s.db.ExecContext(ctx, q); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (s *Service) Schema(ctx context.Context, name string) (*Result, error) {
	return s.rawQuery(ctx, "DESCRIBE "+QuoteIdent(name), nil, 10000)
}

func (s *Service) Preview(ctx context.Context, name string, limit int) (*Result, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > s.cfg.MaxRows {
		limit = s.cfg.MaxRows
	}
	return s.rawQuery(ctx, fmt.Sprintf("SELECT * FROM %s LIMIT %d", QuoteIdent(name), limit), nil, limit)
}

// Query runs a caller-supplied statement, enforcing the read-only guard.
func (s *Service) Query(ctx context.Context, query string, args []any, maxRows int) (*Result, error) {
	if err := s.guard(query); err != nil {
		return nil, err
	}
	if maxRows <= 0 || maxRows > s.cfg.MaxRows {
		maxRows = s.cfg.MaxRows
	}
	return s.rawQuery(ctx, query, args, maxRows)
}

func (s *Service) rawQuery(ctx context.Context, query string, args []any, maxRows int) (*Result, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.QueryTimeout)
	defer cancel()
	start := time.Now()
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types := make([]string, len(cols))
	if colTypes, err := rows.ColumnTypes(); err == nil {
		for i, ct := range colTypes {
			types[i] = ct.DatabaseTypeName()
		}
	}

	res := &Result{Columns: cols, Types: types, Rows: [][]any{}}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if len(res.Rows) >= maxRows {
			res.Truncated = true
			break
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]any, len(cols))
		for i, v := range vals {
			row[i] = normalize(v)
		}
		res.Rows = append(res.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	res.RowCount = len(res.Rows)
	res.DurationMs = time.Since(start).Milliseconds()
	return res, nil
}

// guard rejects non-read statements unless ALLOW_WRITE is enabled.
func (s *Service) guard(query string) error {
	if s.cfg.AllowWrite {
		return nil
	}
	head := strings.ToLower(stripLeadingComments(query))
	word := head
	if i := strings.IndexFunc(head, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '('
	}); i >= 0 {
		word = head[:i]
	}
	switch word {
	case "select", "with", "describe", "desc", "show", "summarize", "explain", "from", "values":
	default:
		return fmt.Errorf("read-only mode: statement %q is not allowed (set ALLOW_WRITE=true to disable the guard)", word)
	}
	trimmed := strings.TrimRight(strings.TrimSpace(query), "; \t\r\n")
	if strings.Contains(trimmed, ";") {
		return errors.New("multiple statements are not allowed")
	}
	return nil
}

func stripLeadingComments(q string) string {
	for {
		q = strings.TrimSpace(q)
		switch {
		case strings.HasPrefix(q, "--"):
			if i := strings.IndexByte(q, '\n'); i >= 0 {
				q = q[i+1:]
				continue
			}
			return ""
		case strings.HasPrefix(q, "/*"):
			if i := strings.Index(q, "*/"); i >= 0 {
				q = q[i+2:]
				continue
			}
			return ""
		}
		return q
	}
}

// ExportMetabase rebuilds <data>/metabase.duckdb (views), <data>/secrets
// (persistent S3 secrets) and <data>/metabase-init.sql so that Metabase can
// query the same tables. The file is written to a temp path and renamed, so a
// Metabase holding the old file open is never disturbed.
func (s *Service) ExportMetabase(ctx context.Context) error {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()

	tables := s.store.List()

	// The secrets dir is fully managed by this exporter: wipe and rebuild so
	// secrets of deleted tables do not linger.
	if entries, err := os.ReadDir(s.secretDir()); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".duckdb_secret") {
				_ = os.Remove(filepath.Join(s.secretDir(), e.Name()))
			}
		}
	}

	target := filepath.Join(s.cfg.DataDir, "metabase.duckdb")
	tmp := target + ".tmp"
	_ = os.Remove(tmp)
	_ = os.Remove(tmp + ".wal")

	connector, err := duckdb.NewConnector(tmp, nil)
	if err != nil {
		return err
	}
	db := sql.OpenDB(connector)
	var firstErr error
	for _, q := range CoreSQL(s.extensionDir(), s.secretDir()) {
		if _, err := db.ExecContext(ctx, q); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%q: %w", q, err)
		}
	}
	for _, t := range tables {
		for _, q := range TableSQL(t, true) {
			if _, err := db.ExecContext(ctx, q); err != nil {
				g.Log().Warningf(ctx, "metabase export: table %q skipped: %v", t.Name, err)
				break
			}
		}
	}
	if err := db.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if firstErr != nil {
		_ = os.Remove(tmp)
		return firstErr
	}
	if err := os.Rename(tmp, target); err != nil {
		return err
	}

	// Plan B for Metabase: an init script creating session secrets, for driver
	// versions where sharing the persistent secret directory is not possible.
	var b strings.Builder
	b.WriteString("INSTALL httpfs;\nLOAD httpfs;\n")
	for _, t := range tables {
		if secret, ok := SecretSQL(t, false); ok {
			b.WriteString(secret + ";\n")
		}
	}
	initPath := filepath.Join(s.cfg.DataDir, "metabase-init.sql")
	if err := os.WriteFile(initPath, []byte(b.String()), 0o600); err != nil {
		g.Log().Warningf(ctx, "write %s: %v", initPath, err)
	}
	g.Log().Infof(ctx, "metabase catalog exported: %s (%d tables)", target, len(tables))
	return nil
}

// normalize converts driver-specific values into JSON-friendly ones.
func normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(x)
	case time.Time:
		return x
	case *big.Int:
		return x
	case duckdb.Decimal:
		return x.Float64()
	case duckdb.Map:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprint(k)] = normalize(val)
		}
		return m
	case map[any]any:
		m := make(map[string]any, len(x))
		for k, val := range x {
			m[fmt.Sprint(k)] = normalize(val)
		}
		return m
	case map[string]any:
		for k, val := range x {
			x[k] = normalize(val)
		}
		return x
	case []any:
		for i := range x {
			x[i] = normalize(x[i])
		}
		return x
	default:
		switch v.(type) {
		case bool, string,
			int, int8, int16, int32, int64,
			uint, uint8, uint16, uint32, uint64,
			float32, float64:
			return v
		}
		if s, ok := v.(fmt.Stringer); ok {
			return s.String()
		}
		return v
	}
}
