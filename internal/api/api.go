package api

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gogf/gf/v2/frame/g"
	"github.com/gogf/gf/v2/net/ghttp"

	"duckdb-api/internal/duck"
	"duckdb-api/internal/model"
	"duckdb-api/internal/store"
)

type handler struct {
	svc      *duck.Service
	st       *store.Store
	apiKey   string
	index    []byte
	localDir string
}

// Register wires all routes onto the GoFrame server.
func Register(s *ghttp.Server, svc *duck.Service, st *store.Store, index []byte, apiKey, localDir string) {
	h := &handler{svc: svc, st: st, apiKey: apiKey, index: index, localDir: localDir}

	s.BindHookHandler("/*any", ghttp.HookBeforeServe, func(r *ghttp.Request) {
		opts := r.Response.DefaultCORSOptions()
		opts.AllowHeaders += ",X-API-Key"
		r.Response.CORS(opts)
		if r.Method == http.MethodOptions {
			r.Response.WriteHeader(http.StatusNoContent)
			r.ExitAll()
		}
	})

	s.BindHandler("GET:/", h.ui)
	s.BindHandler("GET:/healthz", func(r *ghttp.Request) {
		r.Response.WriteJson(g.Map{"status": "ok"})
	})

	s.Group("/api", func(grp *ghttp.RouterGroup) {
		grp.Middleware(h.auth)
		grp.GET("/tables", h.listTables)
		grp.POST("/tables", h.createTable)
		grp.POST("/tables/preview-sql", h.previewSQL)
		grp.GET("/tables/:name", h.getTable)
		grp.PUT("/tables/:name", h.updateTable)
		grp.DELETE("/tables/:name", h.deleteTable)
		grp.GET("/tables/:name/schema", h.tableSchema)
		grp.GET("/tables/:name/preview", h.tablePreview)
		grp.POST("/query", h.query)
		grp.POST("/upload", h.upload)
	})
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._=-]`)

// upload receives a data file (multipart field "file") and stores it in the
// local table directory, so users can query files without shell access to the
// Docker host — useful especially with remote Docker contexts.
func (h *handler) upload(r *ghttp.Request) {
	f := r.GetUploadFile("file")
	if f == nil {
		fail(r, 400, `multipart field "file" is required`)
	}
	name := unsafeFileChars.ReplaceAllString(filepath.Base(f.Filename), "_")
	if name == "" || strings.Trim(name, "._") == "" {
		fail(r, 400, "invalid file name %q", f.Filename)
	}
	f.Filename = name
	saved, err := f.Save(h.localDir)
	if err != nil {
		fail(r, 500, "save upload: %v", err)
	}
	path := filepath.Join(h.localDir, saved)
	if !filepath.IsAbs(path) {
		// Keep a ./ prefix so the path passes table validation as a local path.
		path = "./" + path
	}
	ok(r, g.Map{
		"filename": saved,
		"path":     path,
		"size":     f.Size,
	})
}

func (h *handler) ui(r *ghttp.Request) {
	r.Response.Header().Set("Content-Type", "text/html; charset=utf-8")
	r.Response.Write(h.index)
}

func (h *handler) auth(r *ghttp.Request) {
	if h.apiKey != "" {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			if bearer := r.Header.Get("Authorization"); strings.HasPrefix(bearer, "Bearer ") {
				key = strings.TrimPrefix(bearer, "Bearer ")
			}
		}
		if subtle.ConstantTimeCompare([]byte(key), []byte(h.apiKey)) != 1 {
			r.Response.WriteHeader(http.StatusUnauthorized)
			r.Response.WriteJson(g.Map{"code": 401, "message": "invalid or missing API key (X-API-Key header)"})
			r.ExitAll()
		}
	}
	r.Middleware.Next()
}

// parseJSON decodes the request body as JSON whenever it looks like JSON,
// regardless of the Content-Type header (curl -d defaults to form encoding,
// which would mangle SQL in the payload).
func parseJSON(r *ghttp.Request, v any) error {
	body := bytes.TrimSpace(r.GetBody())
	if len(body) > 0 && (body[0] == '{' || body[0] == '[') {
		return json.Unmarshal(body, v)
	}
	return r.Parse(v)
}

func ok(r *ghttp.Request, data any) {
	r.Response.WriteJson(g.Map{"code": 0, "message": "ok", "data": data})
}

func fail(r *ghttp.Request, status int, format string, args ...any) {
	r.Response.WriteHeader(status)
	r.Response.WriteJson(g.Map{"code": status, "message": fmt.Sprintf(format, args...)})
	r.Exit()
}

func (h *handler) listTables(r *ghttp.Request) {
	tables := h.st.List()
	masked := make([]model.Table, len(tables))
	for i, t := range tables {
		masked[i] = t.Masked()
	}
	ok(r, g.Map{"tables": masked})
}

func (h *handler) getTable(r *ghttp.Request) {
	name := r.GetRouter("name").String()
	t, exists := h.st.Get(name)
	if !exists {
		fail(r, 404, "table %q not found", name)
	}
	ok(r, g.Map{"table": t.Masked()})
}

func (h *handler) createTable(r *ghttp.Request) {
	var t model.Table
	if err := parseJSON(r, &t); err != nil {
		fail(r, 400, "invalid request body: %v", err)
	}
	if err := duck.ValidateTable(&t); err != nil {
		fail(r, 400, "%v", err)
	}
	if _, exists := h.st.Get(t.Name); exists {
		fail(r, 409, "table %q already exists, use PUT /api/tables/%s to update it", t.Name, t.Name)
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	h.applyAndSave(r, t, nil)
}

func (h *handler) updateTable(r *ghttp.Request) {
	name := r.GetRouter("name").String()
	old, exists := h.st.Get(name)
	if !exists {
		fail(r, 404, "table %q not found", name)
	}
	var t model.Table
	if err := parseJSON(r, &t); err != nil {
		fail(r, 400, "invalid request body: %v", err)
	}
	t.Name = name
	t.MergeSecrets(old)
	if err := duck.ValidateTable(&t); err != nil {
		fail(r, 400, "%v", err)
	}
	t.CreatedAt, t.UpdatedAt = old.CreatedAt, time.Now().UTC()
	h.applyAndSave(r, t, &old)
}

// applyAndSave applies the table to DuckDB, persists it and re-exports the
// Metabase catalog. On DuckDB failure during an update, the previous
// definition is restored.
func (h *handler) applyAndSave(r *ghttp.Request, t model.Table, old *model.Table) {
	ctx := r.Context()
	schema, err := h.svc.ApplyTable(ctx, t)
	if err != nil {
		if old != nil {
			if _, rerr := h.svc.ApplyTable(ctx, *old); rerr != nil {
				g.Log().Warningf(ctx, "restore table %q: %v", old.Name, rerr)
			}
		}
		fail(r, 400, "%v", err)
	}
	if err := h.st.Upsert(t); err != nil {
		fail(r, 500, "persist table: %v", err)
	}
	if err := h.svc.ExportMetabase(ctx); err != nil {
		g.Log().Warningf(ctx, "export metabase catalog: %v", err)
	}
	ok(r, g.Map{"table": t.Masked(), "schema": schema})
}

func (h *handler) deleteTable(r *ghttp.Request) {
	ctx := r.Context()
	name := r.GetRouter("name").String()
	if _, exists := h.st.Get(name); !exists {
		fail(r, 404, "table %q not found", name)
	}
	if err := h.st.Delete(name); err != nil {
		fail(r, 500, "persist delete: %v", err)
	}
	if err := h.svc.RemoveTable(ctx, name); err != nil {
		g.Log().Warningf(ctx, "drop table %q: %v", name, err)
	}
	if err := h.svc.ExportMetabase(ctx); err != nil {
		g.Log().Warningf(ctx, "export metabase catalog: %v", err)
	}
	ok(r, g.Map{"deleted": name})
}

func (h *handler) tableSchema(r *ghttp.Request) {
	name := r.GetRouter("name").String()
	if _, exists := h.st.Get(name); !exists {
		fail(r, 404, "table %q not found", name)
	}
	res, err := h.svc.Schema(r.Context(), name)
	if err != nil {
		fail(r, 400, "%v", err)
	}
	ok(r, res)
}

func (h *handler) tablePreview(r *ghttp.Request) {
	name := r.GetRouter("name").String()
	if _, exists := h.st.Get(name); !exists {
		fail(r, 404, "table %q not found", name)
	}
	res, err := h.svc.Preview(r.Context(), name, r.Get("limit").Int())
	if err != nil {
		fail(r, 400, "%v", err)
	}
	ok(r, res)
}

// previewSQL returns the DuckDB statements a table definition would produce,
// with credentials masked — used by the UI to show the equivalent SQL live.
func (h *handler) previewSQL(r *ghttp.Request) {
	var t model.Table
	if err := parseJSON(r, &t); err != nil {
		fail(r, 400, "invalid request body: %v", err)
	}
	// Always generate from the structured fields — this endpoint powers the
	// auto-preview and the "regenerate" action in the UI.
	t.CustomSQL = ""
	if err := duck.ValidateTable(&t); err != nil {
		fail(r, 400, "%v", err)
	}
	ok(r, g.Map{"statements": duck.TableSQL(t.Masked(), false)})
}

type queryRequest struct {
	SQL     string `json:"sql"`
	Args    []any  `json:"args"`
	Format  string `json:"format"` // "" | "rows" | "assoc"
	MaxRows int    `json:"max_rows"`
}

func (h *handler) query(r *ghttp.Request) {
	var req queryRequest
	if err := parseJSON(r, &req); err != nil {
		fail(r, 400, "invalid request body: %v", err)
	}
	if strings.TrimSpace(req.SQL) == "" {
		fail(r, 400, "sql is required")
	}
	res, err := h.svc.Query(r.Context(), req.SQL, normalizeArgs(req.Args), req.MaxRows)
	if err != nil {
		fail(r, 400, "%v", err)
	}
	if req.Format == "assoc" {
		rows := make([]map[string]any, len(res.Rows))
		for i, row := range res.Rows {
			m := make(map[string]any, len(res.Columns))
			for j, col := range res.Columns {
				m[col] = row[j]
			}
			rows[i] = m
		}
		ok(r, g.Map{
			"columns":     res.Columns,
			"types":       res.Types,
			"rows":        rows,
			"row_count":   res.RowCount,
			"truncated":   res.Truncated,
			"duration_ms": res.DurationMs,
		})
		return
	}
	ok(r, res)
}

// normalizeArgs turns JSON integer-valued floats back into int64 so DuckDB
// binds them with the expected type.
func normalizeArgs(args []any) []any {
	for i, a := range args {
		if f, isFloat := a.(float64); isFloat && f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			args[i] = int64(f)
		}
	}
	return args
}
