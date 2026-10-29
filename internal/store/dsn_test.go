package store_test

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"shepherd/internal/config"
	"shepherd/internal/store"
)

// dsnOps runs every entry point that takes a database URL and returns each
// one's error, keyed by name. None of them may succeed for the DSNs these
// specs feed it.
func dsnOps(ctx context.Context, dsn string) map[string]error {
	errs := map[string]error{
		"MigrateUp":       store.MigrateUp(ctx, dsn),
		"MigrateDown":     store.MigrateDown(ctx, dsn),
		"MigrateTo":       store.MigrateTo(ctx, dsn, 1),
		"MigrateStatusTo": store.MigrateStatusTo(ctx, &bytes.Buffer{}, dsn),
	}
	st, err := store.New(ctx, &config.DatabaseConfig{URL: dsn, MaxConns: 1})
	if st != nil {
		st.Close()
	}
	errs["store.New"] = err
	return errs
}

func expectNoFragment(errs map[string]error, fragments ...string) {
	GinkgoHelper()
	for op, err := range errs {
		Expect(err).To(HaveOccurred(), "%s must fail", op)
		for _, f := range fragments {
			Expect(err.Error()).NotTo(ContainSubstring(f), "%s's error quotes part of the password", op)
		}
	}
}

var _ = Describe("Database URL parsing", func() {
	DescribeTable("refuses a URL whose credentials an unescaped character cut short, naming no part of it",
		func(dsn string, fragments ...string) {
			_, err := store.ParseDatabaseURL(dsn)
			Expect(err).To(MatchError(store.ErrAmbiguousDatabaseURL))

			expectNoFragment(dsnOps(context.Background(), dsn), fragments...)
			for op, err := range dsnOps(context.Background(), dsn) {
				Expect(err).To(MatchError(store.ErrAmbiguousDatabaseURL), op)
			}
		},
		Entry("'/' in the password, letters after it", "postgres://shepherd:zebra/twelve@db.invalid/shepherd", "zebra", "twelve"),
		Entry("password starting with '/'", "postgres://shepherd:/hunter@db.invalid/shepherd", "hunter"),
		Entry("digits around '/' (reads as a port)", "postgres://shepherd:12/34@db.invalid/shepherd", "12/34", "34@db"),
		Entry("'@' then '/' in the password", "postgres://shepherd:p@ss/word@db.invalid/shepherd", "ss/word", "word@db"),
		Entry("'@' in the password, no '/'", "postgres://shepherd:p@ss@db.invalid/shepherd", "ss@db"),
		Entry("'/' then '?' in the password", "postgres://shepherd:ab/cd?ef@db.invalid/shepherd", "cd?ef", "ef@db"),
		Entry("'/' then '#' in the password", "postgres://shepherd:ab/cd#ef@db.invalid/shepherd", "cd#ef", "ef@db"),
		Entry("'/' in the user name", "postgres://do/main:secret@db.invalid/shepherd", "secret"),
		Entry("postgresql:// scheme", "postgresql://shepherd:zebra/twelve@db.invalid/shepherd", "zebra", "twelve"),
		Entry("pgx5:// scheme", "pgx5://shepherd:zebra/twelve@db.invalid/shepherd", "zebra", "twelve"),
		Entry("no path, ?password= value with '@'", "postgres://db.invalid?password=p@zq", "zq"),
	)

	DescribeTable("never quotes the password when the URL parses but the connection fails",
		func(dsn string, fragments ...string) {
			expectNoFragment(dsnOps(context.Background(), dsn), fragments...)
		},
		Entry("'#' in the password, unreachable host", "postgres://shepherd:a#b@db.invalid/shepherd?connect_timeout=2", "a#b"),
		Entry("'?' in the password, unreachable host", "postgres://shepherd:a?b@db.invalid/shepherd?connect_timeout=2", "a?b"),
		Entry("encoded '/', closed port", "postgres://shepherd:zebra%2Ftwelve@127.0.0.1:1/shepherd?connect_timeout=2", "zebra", "twelve"),
		Entry("query password, closed port", "postgres://shepherd@127.0.0.1:1/shepherd?password=p@ss/word&connect_timeout=2", "ss/word"),
		Entry("keyword/value, closed port", "host=127.0.0.1 port=1 user=shepherd password='p@ss/word' dbname=shepherd connect_timeout=2", "ss/word"),
	)

	DescribeTable("keeps every unambiguous URL shape",
		func(dsn string, want map[string]any) {
			cfg, err := store.ParseDatabaseURL(dsn)
			Expect(err).NotTo(HaveOccurred())
			c := cfg.ConnConfig
			got := map[string]any{"host": c.Host, "port": int(c.Port), "user": c.User, "password": c.Password, "database": c.Database}
			for k, v := range want {
				if k == "fallbacks" {
					hosts := make([]string, 0, len(c.Fallbacks))
					for _, f := range c.Fallbacks {
						hosts = append(hosts, fmt.Sprintf("%s:%d", f.Host, f.Port))
					}
					// pgx lists every host here, once per TLS attempt.
					Expect(hosts).To(ContainElements(v), k)
					continue
				}
				Expect(got[k]).To(Equal(v), k)
			}
		},
		Entry("plain", "postgres://u:pw@db.example:5432/app",
			map[string]any{"host": "db.example", "port": 5432, "user": "u", "password": "pw", "database": "app"}),
		Entry("postgresql:// scheme", "postgresql://u:pw@db.example/app",
			map[string]any{"host": "db.example", "user": "u", "password": "pw", "database": "app"}),
		Entry("pgx5:// scheme", "pgx5://u:pw@db.example/app",
			map[string]any{"host": "db.example", "user": "u", "password": "pw", "database": "app"}),
		Entry("missing port", "postgres://u:pw@db.example/app",
			map[string]any{"host": "db.example", "port": 5432}),
		Entry("percent-encoded userinfo", "postgres://u%40corp:p%40ss%2Fword@db.example/app",
			map[string]any{"user": "u@corp", "password": "p@ss/word", "host": "db.example", "database": "app"}),
		Entry("':' in the password", "postgres://u:a:b@db.example/app",
			map[string]any{"user": "u", "password": "a:b", "host": "db.example"}),
		Entry("'?' and '#' in the password", "postgres://u:a?b#c@db.example/app",
			map[string]any{"user": "u", "password": "a?b#c", "host": "db.example", "database": "app"}),
		Entry("empty password", "postgres://u:@db.example/app",
			map[string]any{"user": "u", "password": "", "host": "db.example", "database": "app"}),
		Entry("'@' in a query value", "postgres://u:pw@db.example/app?application_name=ops@example",
			map[string]any{"user": "u", "password": "pw", "host": "db.example", "database": "app"}),
		Entry("query-parameter password with '@' and '/'", "postgres://u@db.example/app?password=p@ss/word",
			map[string]any{"user": "u", "password": "p@ss/word", "host": "db.example", "database": "app"}),
		Entry("IPv6 host", "postgres://u:pw@[::1]:5433/app",
			map[string]any{"host": "::1", "port": 5433, "user": "u", "password": "pw", "database": "app"}),
		Entry("multiple hosts", "postgres://u:pw@db1.example:5432,db2.example:5433/app",
			map[string]any{"host": "db1.example", "port": 5432, "fallbacks": []string{"db2.example:5433"}}),
		Entry("unix socket via ?host=", "postgres://u:pw@/app?host=/var/run/postgresql",
			map[string]any{"host": "/var/run/postgresql", "user": "u", "password": "pw", "database": "app"}),
		Entry("keyword/value", "host=db.example port=5433 user=u password='p@ss/word' dbname=app",
			map[string]any{"host": "db.example", "port": 5433, "user": "u", "password": "p@ss/word", "database": "app"}),
	)
})

var dsnRoleSeq atomic.Int64

var _ = Describe("Migrating with every URL shape the server accepts", Label("integration"), func() {
	var (
		ctx      context.Context
		hostPort string
		host     string
		port     string
		dbname   string
		admin    *pgxpool.Pool
	)

	BeforeEach(func() {
		ctx = context.Background()
		root, err := url.Parse(sharedPG.RootURL)
		Expect(err).NotTo(HaveOccurred())
		host, port, hostPort = root.Hostname(), root.Port(), root.Host

		iso, err := url.Parse(sharedPG.IsolatedDB(ctx, GinkgoTB()))
		Expect(err).NotTo(HaveOccurred())
		dbname = strings.TrimPrefix(iso.Path, "/")

		adminURL := *root
		adminURL.Path = "/postgres"
		admin, err = pgxpool.New(ctx, adminURL.String())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(admin.Close)
	})

	// role creates a login role with the given password and returns its name.
	role := func(password string) string {
		name := fmt.Sprintf("dsn_spec_%d_%d", GinkgoParallelProcess(), dsnRoleSeq.Add(1))
		_, err := admin.Exec(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN SUPERUSER PASSWORD '%s'", name, password))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			_, _ = admin.Exec(context.Background(), //nolint:errcheck // best-effort cleanup
				fmt.Sprintf(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = '%s'`, name))
			_, _ = admin.Exec(context.Background(), "DROP ROLE IF EXISTS "+name) //nolint:errcheck // best-effort cleanup
		})
		return name
	}

	expectWorks := func(dsn string) {
		GinkgoHelper()
		Expect(store.MigrateUp(ctx, dsn)).To(Succeed())
		var buf bytes.Buffer
		Expect(store.MigrateStatusTo(ctx, &buf, dsn)).To(Succeed())
		Expect(buf.String()).To(MatchRegexp(`^version=\d+ dirty=false\n$`))
		st, err := store.New(ctx, &config.DatabaseConfig{URL: dsn, MaxConns: 1})
		Expect(err).NotTo(HaveOccurred())
		st.Close()
	}

	It("accepts a percent-encoded password", func() {
		r := role("p@ss/word")
		expectWorks(fmt.Sprintf("postgres://%s:p%%40ss%%2Fword@%s/%s", r, hostPort, dbname))
	})

	It("accepts '?', '#' and ':' unescaped in the password", func() {
		r := role("a?b#c:d")
		expectWorks(fmt.Sprintf("postgres://%s:a?b#c:d@%s/%s", r, hostPort, dbname))
	})

	It("accepts a query-parameter password containing '@' and '/'", func() {
		r := role("p@ss/word")
		expectWorks(fmt.Sprintf("postgres://%s@%s/%s?password=p@ss/word", r, hostPort, dbname))
	})

	It("accepts a literal '@' in a query value", func() {
		r := role("plainpw")
		expectWorks(fmt.Sprintf("postgres://%s:plainpw@%s/%s?application_name=ops@example", r, hostPort, dbname))
	})

	It("accepts the postgresql:// and pgx5:// schemes", func() {
		r := role("plainpw")
		expectWorks(fmt.Sprintf("postgresql://%s:plainpw@%s/%s", r, hostPort, dbname))
		expectWorks(fmt.Sprintf("pgx5://%s:plainpw@%s/%s", r, hostPort, dbname))
	})

	It("accepts a keyword/value DSN", func() {
		r := role("p@ss/word")
		expectWorks(fmt.Sprintf("host=%s port=%s user=%s password='p@ss/word' dbname=%s", host, port, r, dbname))
	})

	It("accepts multiple hosts, falling back past an unreachable one", func() {
		r := role("plainpw")
		expectWorks(fmt.Sprintf("postgres://%s:plainpw@127.0.0.1:1,%s/%s?connect_timeout=5", r, hostPort, dbname))
	})

	It("names no password when the password is wrong or the host unreachable", func() {
		r := role("plainpw")
		expectNoFragment(dsnOps(ctx, fmt.Sprintf("postgres://%s:wrongpw-a?b@%s/%s", r, hostPort, dbname)), "wrongpw", "a?b")
		expectNoFragment(dsnOps(ctx, fmt.Sprintf("postgres://%s:wrongpw-a%%2Fb@%s/%s", r, hostPort, dbname)), "wrongpw")
		expectNoFragment(dsnOps(ctx, fmt.Sprintf("postgres://%s:wrongpw@127.0.0.1:1/%s?connect_timeout=2", r, dbname)), "wrongpw")
	})

	It("records the version in the same public.schema_migrations table as before", func() {
		r := role("plainpw")
		expectWorks(fmt.Sprintf("host=%s port=%s user=%s password=plainpw dbname=%s", host, port, r, dbname))
		st, err := store.New(ctx, &config.DatabaseConfig{URL: fmt.Sprintf("postgres://%s:plainpw@%s/%s", r, hostPort, dbname), MaxConns: 1})
		Expect(err).NotTo(HaveOccurred())
		defer st.Close()
		var tables []string
		rows, err := st.Pool().Query(ctx,
			`SELECT table_schema || '.' || table_name FROM information_schema.tables WHERE table_name LIKE '%migrations%'`)
		Expect(err).NotTo(HaveOccurred())
		for rows.Next() {
			var t string
			Expect(rows.Scan(&t)).To(Succeed())
			tables = append(tables, t)
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		Expect(tables).To(Equal([]string{"public.schema_migrations"}))
	})
})
