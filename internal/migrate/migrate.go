// Package migrate applies ordered SQL migration files to a project's Postgres
// database, tracking what has run in a ledger table so re-runs are idempotent.
package migrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
)

// LedgerTable records which migrations have been applied. service_role/owner
// only — it lives in the project's own database.
const LedgerTable = "kethosbase_migrations"

const ledgerDDL = `create table if not exists ` + LedgerTable + ` (
	version    text primary key,
	checksum   text        not null,
	applied_at timestamptz not null default now()
)`

// Migration is one .sql file on disk.
type Migration struct {
	Version  string // the file name without ".sql", used as the sort key + id
	Path     string
	SQL      string
	Checksum string // sha256 hex of the file contents
}

// Discover reads every *.sql file in dir, in lexical (i.e. zero-padded numeric)
// order. Names like 0001_init.sql sort correctly as-is.
func Discover(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var migs []Migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		migs = append(migs, Migration{
			Version:  strings.TrimSuffix(e.Name(), ".sql"),
			Path:     filepath.Join(dir, e.Name()),
			SQL:      string(b),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].Version < migs[j].Version })
	return migs, nil
}

// NextFilename returns a migration file name that sorts after every existing
// *.sql file in dir, using the same zero-padded numeric convention the runner
// discovers (e.g. 0003_<slug>.sql after 0001_… and 0002_…). It only reads names,
// so a missing dir simply yields 0001_<slug>.sql. The slug is sanitised to
// [a-z0-9_]; an empty slug falls back to "migration".
func NextFilename(dir, slug string) string {
	slug = sanitiseSlug(slug)
	next := 1
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".sql")
			num := leadingNumber(name)
			if num >= next {
				next = num + 1
			}
		}
	}
	return fmt.Sprintf("%04d_%s.sql", next, slug)
}

func leadingNumber(name string) int {
	end := 0
	for end < len(name) && name[end] >= '0' && name[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	n := 0
	for _, r := range name[:end] {
		n = n*10 + int(r-'0')
	}
	return n
}

func sanitiseSlug(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	prevUnderscore := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			prevUnderscore = false
		case r == '_' || r == '-' || r == ' ':
			if !prevUnderscore && b.Len() > 0 {
				b.WriteByte('_')
				prevUnderscore = true
			}
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "migration"
	}
	return out
}

// Status is the applied/pending split for the linked database.
type Status struct {
	Applied []string // versions already in the ledger, in order
	Pending []string // versions on disk not yet applied, in order
}

func appliedChecksums(ctx context.Context, conn *pgx.Conn) (map[string]string, error) {
	if _, err := conn.Exec(ctx, ledgerDDL); err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, `select version, checksum from `+LedgerTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var v, c string
		if err := rows.Scan(&v, &c); err != nil {
			return nil, err
		}
		out[v] = c
	}
	return out, rows.Err()
}

// GetStatus reports which migrations are applied vs pending, and verifies that
// already-applied files have not changed on disk (checksum drift).
func GetStatus(ctx context.Context, connURL, dir string) (*Status, error) {
	migs, err := Discover(dir)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	applied, err := appliedChecksums(ctx, conn)
	if err != nil {
		return nil, err
	}
	st := &Status{}
	for _, m := range migs {
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Checksum {
				return nil, fmt.Errorf("migration %q changed after being applied (checksum drift) — never edit an applied migration; add a new one", m.Version)
			}
			st.Applied = append(st.Applied, m.Version)
		} else {
			st.Pending = append(st.Pending, m.Version)
		}
	}
	return st, nil
}

// Up applies every pending migration in order, each in its own transaction, and
// records it in the ledger. It returns the versions it applied.
func Up(ctx context.Context, connURL, dir string) ([]string, error) {
	migs, err := Discover(dir)
	if err != nil {
		return nil, err
	}
	conn, err := pgx.Connect(ctx, connURL)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(ctx)

	applied, err := appliedChecksums(ctx, conn)
	if err != nil {
		return nil, err
	}

	var done []string
	for _, m := range migs {
		if sum, ok := applied[m.Version]; ok {
			if sum != m.Checksum {
				return done, fmt.Errorf("migration %q changed after being applied (checksum drift) — never edit an applied migration; add a new one", m.Version)
			}
			continue // already applied
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return done, fmt.Errorf("apply %q: %w", m.Version, err)
		}
		done = append(done, m.Version)
	}
	return done, nil
}

func applyOne(ctx context.Context, conn *pgx.Conn, m Migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) // no-op once committed

	if _, err := tx.Exec(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`insert into `+LedgerTable+` (version, checksum) values ($1, $2)`,
		m.Version, m.Checksum,
	); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
