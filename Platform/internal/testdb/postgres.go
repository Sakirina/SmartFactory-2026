// Package testdb isolates integration fixtures without touching shared schemas.
package testdb

import (
	"context"
	"database/sql"
	"fmt"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Postgres uses one management connection. Callers cap their application pool
// at three connections, keeping each fixture within four live connections.
func Postgres(t *testing.T, prefix string) (string, *sql.DB) {
	t.Helper()
	dsn := os.Getenv("SF_TEST_POSTGRES_DATABASE")
	if dsn == "" {
		t.Skip("isolated PostgreSQL integration DSN required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin.SetMaxOpenConns(1)
	admin.SetMaxIdleConns(1)
	name := fmt.Sprintf("sf_%s_%d", prefix, time.Now().UnixNano())
	name = strings.ReplaceAll(name, "-", "_")
	if _, err = admin.ExecContext(context.Background(), "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.ExecContext(ctx, "DROP SCHEMA "+name+" CASCADE"); err != nil {
			t.Errorf("fixture cleanup: %v", err)
		}
		admin.Close()
	})
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	return u.String(), admin
}
