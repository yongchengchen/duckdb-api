package duck

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"duckdb-api/internal/model"
)

var (
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	numRe   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?$`)

	readerFuncs = map[string]string{
		"parquet": "read_parquet",
		"csv":     "read_csv_auto",
		"json":    "read_json_auto",
	}
)

func QuoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func QuoteString(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// ValidateTable normalizes and validates a table definition.
func ValidateTable(t *model.Table) error {
	t.Name = strings.TrimSpace(t.Name)
	if !identRe.MatchString(t.Name) {
		return fmt.Errorf("invalid table name %q: must match [A-Za-z_][A-Za-z0-9_]*", t.Name)
	}
	t.Format = strings.ToLower(strings.TrimSpace(t.Format))
	if t.Format == "" {
		t.Format = "parquet"
	}
	if _, ok := readerFuncs[t.Format]; !ok {
		return fmt.Errorf("unsupported format %q: use parquet, csv or json", t.Format)
	}
	t.Path = strings.TrimSpace(t.Path)
	if t.Path == "" {
		return errors.New("path is required")
	}
	lower := strings.ToLower(t.Path)
	isS3 := strings.HasPrefix(lower, "s3://")
	isHTTP := strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
	isLocal := strings.HasPrefix(t.Path, "/") || strings.HasPrefix(t.Path, "./")
	if !isS3 && !isHTTP && !isLocal {
		return fmt.Errorf("unsupported path %q: use s3://, http(s)://, or a local path inside the container (e.g. /local/orders/*.parquet)", t.Path)
	}
	if isS3 {
		if t.S3 == nil || t.S3.AccessKeyID == "" || t.S3.SecretAccessKey == "" || t.S3.Region == "" {
			return errors.New("s3:// paths require s3.region, s3.access_key_id and s3.secret_access_key")
		}
	}
	if t.S3 != nil {
		switch t.S3.URLStyle {
		case "", "path", "vhost":
		default:
			return fmt.Errorf("invalid s3.url_style %q: use \"path\" or \"vhost\"", t.S3.URLStyle)
		}
		t.S3.Endpoint = strings.TrimSpace(t.S3.Endpoint)
		if strings.Contains(t.S3.Endpoint, "://") {
			return errors.New("s3.endpoint must be host[:port] without scheme, e.g. s3.ap-southeast-2.amazonaws.com")
		}
	}
	for k := range t.Options {
		if !identRe.MatchString(k) {
			return fmt.Errorf("invalid option key %q", k)
		}
	}
	return nil
}

func SecretName(table string) string {
	return "secret_" + table
}

// secretScope returns "s3://bucket" derived from the path, or "" if the bucket
// part contains wildcards.
func secretScope(path string) string {
	rest := strings.TrimPrefix(path, "s3://")
	bucket := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		bucket = rest[:i]
	}
	if bucket == "" || strings.ContainsAny(bucket, "*?[") {
		return ""
	}
	return "s3://" + bucket
}

// SecretSQL builds the CREATE SECRET statement for a table, if it needs one.
func SecretSQL(t model.Table, persistent bool) (string, bool) {
	if t.S3 == nil || !strings.HasPrefix(strings.ToLower(t.Path), "s3://") {
		return "", false
	}
	var b strings.Builder
	b.WriteString("CREATE OR REPLACE ")
	if persistent {
		b.WriteString("PERSISTENT ")
	}
	b.WriteString("SECRET ")
	b.WriteString(QuoteIdent(SecretName(t.Name)))
	b.WriteString(" (\n    TYPE s3")
	add := func(key, val string) {
		if val != "" {
			fmt.Fprintf(&b, ",\n    %s %s", key, QuoteString(val))
		}
	}
	add("KEY_ID", t.S3.AccessKeyID)
	add("SECRET", t.S3.SecretAccessKey)
	add("REGION", t.S3.Region)
	add("SESSION_TOKEN", t.S3.SessionToken)
	add("ENDPOINT", t.S3.Endpoint)
	add("URL_STYLE", t.S3.URLStyle)
	if t.S3.UseSSL != nil {
		fmt.Fprintf(&b, ",\n    USE_SSL %t", *t.S3.UseSSL)
	}
	add("SCOPE", secretScope(t.Path))
	b.WriteString("\n)")
	return b.String(), true
}

// ReaderSQL builds the read_parquet/read_csv_auto/read_json_auto call.
func ReaderSQL(t model.Table) string {
	fn := readerFuncs[t.Format]
	args := []string{QuoteString(t.Path)}
	if t.HivePartitioning {
		args = append(args, "hive_partitioning = true")
	}
	keys := make([]string, 0, len(t.Options))
	for k := range t.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		args = append(args, k+" = "+optionLiteral(t.Options[k]))
	}
	return fn + "(" + strings.Join(args, ", ") + ")"
}

func optionLiteral(v string) string {
	lower := strings.ToLower(strings.TrimSpace(v))
	if lower == "true" || lower == "false" || numRe.MatchString(lower) {
		return lower
	}
	return QuoteString(v)
}

func ViewSQL(t model.Table) string {
	return "CREATE OR REPLACE VIEW " + QuoteIdent(t.Name) + " AS SELECT * FROM " + ReaderSQL(t)
}

// TableSQL returns all statements needed to (re)register one table.
func TableSQL(t model.Table, persistentSecret bool) []string {
	var out []string
	if secret, ok := SecretSQL(t, persistentSecret); ok {
		out = append(out, secret)
	}
	return append(out, ViewSQL(t))
}

// DropTableSQL returns statements removing a table registration.
func DropTableSQL(name string) []string {
	return []string{
		"DROP VIEW IF EXISTS " + QuoteIdent(name),
		"DROP SECRET IF EXISTS " + QuoteIdent(SecretName(name)),
	}
}

// CoreSQL returns the session/database bootstrap statements.
func CoreSQL(extensionDir, secretDir string) []string {
	var out []string
	if extensionDir != "" {
		out = append(out, "SET extension_directory = "+QuoteString(extensionDir))
	}
	if secretDir != "" {
		out = append(out, "SET secret_directory = "+QuoteString(secretDir))
	}
	return append(out, "INSTALL httpfs", "LOAD httpfs")
}
