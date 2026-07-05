package model

import "time"

// MaskedSecret is the placeholder returned by the API instead of real credentials.
const MaskedSecret = "********"

// S3Config holds the S3-compatible credentials/settings for a table source.
// Endpoint is host[:port] without scheme (e.g. "s3.ap-southeast-2.amazonaws.com",
// or an R2/MinIO endpoint). URLStyle is "vhost" or "path".
type S3Config struct {
	Region          string `json:"region"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token,omitempty"`
	Endpoint        string `json:"endpoint,omitempty"`
	URLStyle        string `json:"url_style,omitempty"`
	UseSSL          *bool  `json:"use_ssl,omitempty"`
}

// Table is a registered external table: a DuckDB view over a file source.
type Table struct {
	Name             string            `json:"name"`
	Format           string            `json:"format"` // parquet | csv | json
	Path             string            `json:"path"`   // s3://..., https://..., or a local path
	HivePartitioning bool              `json:"hive_partitioning"`
	S3               *S3Config         `json:"s3,omitempty"`
	Options          map[string]string `json:"options,omitempty"` // extra reader options, passed as key = value
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

// Masked returns a copy safe to return from the API (credentials hidden).
func (t Table) Masked() Table {
	if t.S3 != nil {
		s3 := *t.S3
		if s3.SecretAccessKey != "" {
			s3.SecretAccessKey = MaskedSecret
		}
		if s3.SessionToken != "" {
			s3.SessionToken = MaskedSecret
		}
		t.S3 = &s3
	}
	return t
}

// MergeSecrets fills masked or blank credentials from the previous version of
// the table, so clients can update a table without re-sending secrets.
func (t *Table) MergeSecrets(old Table) {
	if t.S3 == nil || old.S3 == nil {
		return
	}
	if t.S3.SecretAccessKey == "" || t.S3.SecretAccessKey == MaskedSecret {
		t.S3.SecretAccessKey = old.S3.SecretAccessKey
	}
	if t.S3.SessionToken == MaskedSecret {
		t.S3.SessionToken = old.S3.SessionToken
	}
	if t.S3.AccessKeyID == "" {
		t.S3.AccessKeyID = old.S3.AccessKeyID
	}
	if t.S3.Region == "" {
		t.S3.Region = old.S3.Region
	}
}
