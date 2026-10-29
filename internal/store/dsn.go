package store

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrAmbiguousDatabaseURL is returned for a database URL whose user name or
// password was cut short by an unescaped character. Its text is fixed and
// names no part of the URL: the parts a misparse produces (host, database,
// user) are pieces of the password, and every error built from them would
// quote one.
var ErrAmbiguousDatabaseURL = errors.New("database URL could not be parsed unambiguously; " +
	"percent-encode special characters in the user name, password and database name (e.g. '/' as %2F, '@' as %40)")

// parseDatabaseURL is the one place a database URL (or keyword/value DSN) is
// parsed, shared by New and the migrator so both accept exactly the same
// inputs. pgx parses it; pgx's own errors mask the password. A pgx5:// URL
// (golang-migrate's scheme for its pgx driver, which the migrator used to
// require internally) is read as postgres://.
func parseDatabaseURL(databaseURL string) (*pgxpool.Config, error) {
	dsn := databaseURL
	if rest, ok := strings.CutPrefix(dsn, "pgx5://"); ok {
		dsn = "postgres://" + rest
	}
	if misreadsCredentials(dsn) {
		return nil, ErrAmbiguousDatabaseURL
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parsing database URL: %w", err)
	}
	return cfg, nil
}

// misreadsCredentials reports whether pgx would split a URL-form DSN somewhere
// other than where its author meant, so that part of the password lands in
// the host, database name, user name or a query key — fields that connection
// errors quote.
//
// pgx follows libpq: the userinfo ends at the first '@' unless a '/' comes
// first, so a '?', '#' or ':' inside a password parses correctly, but an
// unescaped '/' or '@' moves the split. When that happens a '@' that belonged
// before the host is left in a place where a raw '@' never means anything:
//
//   - the host list (postgres://u:p@ss@host/db: host "ss@host");
//   - the database name (postgres://u:/pw@host/db, postgres://u:12/34@host/db,
//     postgres://u:p@ss/word@host/db: database "...@host/db");
//   - a query key (postgres://u:a/b?c@host/db: key "c@host/db").
//
// A '@' in a query value (?application_name=ops@example, ?password=p@ss/word)
// is left alone. One more shape is caught: a URL with no path whose
// ?password= value holds a '@' (postgres://host?password=p@ss), where pgx reads
// everything up to that '@' as the userinfo.
//
// Keyword/value DSNs have no such ambiguity and are not inspected.
func misreadsCredentials(dsn string) bool {
	p, ok := strings.CutPrefix(dsn, "postgresql://")
	if !ok {
		p, ok = strings.CutPrefix(dsn, "postgres://")
	}
	if !ok {
		return false
	}

	if i := strings.IndexAny(p, "@/"); i >= 0 && p[i] == '@' {
		if userinfoHoldsQuery(p[:i]) {
			return true
		}
		p = p[i+1:]
	}

	hostEnd := strings.IndexAny(p, "/?")
	if hostEnd < 0 {
		hostEnd = len(p)
	}
	if strings.Contains(p[:hostEnd], "@") {
		return true
	}

	rest := p[hostEnd:]
	var query string
	switch {
	case strings.HasPrefix(rest, "/"):
		dbname, q, _ := strings.Cut(rest[1:], "?")
		if strings.Contains(dbname, "@") {
			return true
		}
		query = q
	case strings.HasPrefix(rest, "?"):
		query = rest[1:]
	}
	for pair := range strings.SplitSeq(query, "&") {
		key, _, _ := strings.Cut(pair, "=")
		if strings.Contains(key, "@") {
			return true
		}
	}
	return false
}

// userinfoHoldsQuery reports whether what pgx took as the userinfo is really
// a host followed by a query carrying a password (postgres://host?password=p@ss
// has no '/', so pgx reads "host?password=p" as the userinfo).
func userinfoHoldsQuery(userinfo string) bool {
	_, query, found := strings.Cut(userinfo, "?")
	if !found {
		return false
	}
	for pair := range strings.SplitSeq(query, "&") {
		key, _, hasValue := strings.Cut(pair, "=")
		if hasValue && (strings.EqualFold(key, "password") || strings.EqualFold(key, "sslpassword")) {
			return true
		}
	}
	return false
}
