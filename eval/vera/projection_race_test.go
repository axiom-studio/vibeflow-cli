//go:build darwin || linux

package vera

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Defect h-tag-projection-stale-snapshot, reproduced: two transactions run
// the fixture's own UpsertTag SQL in the order a concurrent pair can take,
// against a throwaway local PostgreSQL cluster (loopback TCP only, trust
// auth, no shared instance, no credentials). Both tag rows survive, the
// tags_json projection keeps only the second, and the fixture's recall
// filter then rejects the memory on the first tag. The decoy, whose writers
// take the note's row lock first, ends with both labels in its projection.
//
// Needs initdb, pg_ctl and psql on PATH; skipped without them or with -short.
func TestTagProjectionRaceReproduces(t *testing.T) {
	pg := startPostgres(t)
	fixture := buildFixture(t)
	tagSQL := fixtureConsts(t, filepath.Join(fixture, "tags", "store.go"))
	labelSQL := fixtureConsts(t, filepath.Join(fixture, "notes", "labels.go"))

	pg.exec(t, tagSQL["Schema"]+labelSQL["Schema"]+`
INSERT INTO memories(id, workspace_id, content) VALUES ('m1', 'w1', 'deploy notes');
INSERT INTO notes(id, project_id, body) VALUES ('n1', 'p1', 'release plan');`)

	t.Run("defect: unlocked projection rewrite", func(t *testing.T) {
		s1, s2 := pg.session(t), pg.session(t)
		for _, s := range []*psqlSession{s1, s2} {
			s.run(t, fmt.Sprintf("PREPARE ins(text, text, text, text) AS %s;\nPREPARE sync(text) AS %s;", tagSQL["insertTagSQL"], tagSQL["syncTagsSQL"]))
		}
		s1.run(t, "BEGIN; EXECUTE ins('m1', 'first', 'one', 'w1'); EXECUTE sync('m1');")
		s2.run(t, "BEGIN; EXECUTE ins('m1', 'second', 'two', 'w1');")
		pending := s2.start(t, "EXECUTE sync('m1');")
		pg.waitForLockWait(t)
		s1.run(t, "COMMIT;")
		pending.wait(t)
		s2.run(t, "COMMIT;")

		if rows := pg.query(t, "SELECT key || '=' || value FROM tags WHERE memory_id = 'm1' ORDER BY key"); strings.Join(rows, ",") != "first=one,second=two" {
			t.Fatalf("tag rows = %v, want both", rows)
		}
		projection := strings.Join(pg.query(t, "SELECT tags_json::text FROM memories WHERE id = 'm1'"), "")
		if projection != `{"second": "two"}` {
			t.Fatalf("tags_json = %s, want the stale {\"second\": \"two\"}", projection)
		}
		// The fixture's own recall filter, run on the returned projection.
		if got := runMatches(t, fixture, projection, "first", "one"); got != "false" {
			t.Fatalf("tags.Matches(first=one) on the stale projection = %s, want false", got)
		}
		if got := runMatches(t, fixture, projection, "second", "two"); got != "true" {
			t.Fatalf("tags.Matches(second=two) = %s, want true", got)
		}
	})

	t.Run("decoy: row lock before the change", func(t *testing.T) {
		s1, s2 := pg.session(t), pg.session(t)
		for _, s := range []*psqlSession{s1, s2} {
			s.run(t, fmt.Sprintf("PREPARE lck(text, text) AS %s;\nPREPARE ins(text, text, text) AS %s;\nPREPARE sync(text) AS %s;", labelSQL["lockNoteSQL"], labelSQL["insertLabelSQL"], labelSQL["syncLabelsSQL"]))
		}
		s1.run(t, "BEGIN; EXECUTE lck('n1', 'p1'); EXECUTE ins('n1', 'first', 'red'); EXECUTE sync('n1');")
		s2.run(t, "BEGIN;")
		pending := s2.start(t, "EXECUTE lck('n1', 'p1');")
		pg.waitForLockWait(t)
		s1.run(t, "COMMIT;")
		pending.wait(t)
		s2.run(t, "EXECUTE ins('n1', 'second', 'blue'); EXECUTE sync('n1'); COMMIT;")

		if projection := strings.Join(pg.query(t, "SELECT labels_json::text FROM notes WHERE id = 'n1'"), ""); projection != `{"first": "red", "second": "blue"}` {
			t.Fatalf("labels_json = %s, want both labels", projection)
		}
	})
}

// fixtureConsts returns the string constants a fixture Go file declares, so
// the reproduction runs the seeded code's SQL rather than a copy of it.
func fixtureConsts(t *testing.T, path string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			for i, name := range value.Names {
				if lit, ok := value.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if out[name.Name], err = strconv.Unquote(lit.Value); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	return out
}

// runMatches runs the fixture's tags.Matches on a tags_json value.
func runMatches(t *testing.T, fixture, tagsJSON, key, value string) string {
	t.Helper()
	dir := filepath.Join(fixture, "internal", "matchcheck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	program := `package main

import (
	"fmt"
	"os"

	"example.com/reviewfixture/tags"
)

func main() { fmt.Print(tags.Matches([]byte(os.Args[1]), map[string]string{os.Args[2]: os.Args[3]})) }
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(program), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "./internal/matchcheck", tagsJSON, key, value)
	cmd.Dir, cmd.Env = fixture, append(os.Environ(), "GOWORK=off", "GOFLAGS=", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run matchcheck: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

type postgres struct {
	port string
	env  []string
}

func startPostgres(t *testing.T) *postgres {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a PostgreSQL cluster")
	}
	for _, tool := range []string{"initdb", "pg_ctl", "psql"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH", tool)
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	listener.Close()
	// Only PATH and a fixed locale reach the server and clients.
	pg := &postgres{port: port, env: []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}}
	data := filepath.Join(t.TempDir(), "data")
	pg.tool(t, "initdb", "-D", data, "-U", "postgres", "-A", "trust", "--no-locale", "-E", "UTF8")
	pg.tool(t, "pg_ctl", "-D", data, "-l", filepath.Join(data, "server.log"), "-w", "-o",
		"-c listen_addresses=127.0.0.1 -p "+port+" -c unix_socket_directories='' -c fsync=off", "start")
	t.Cleanup(func() { _ = exec.Command("pg_ctl", "-D", data, "-m", "immediate", "-w", "stop").Run() })
	return pg
}

func (pg *postgres) tool(t *testing.T, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = pg.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", name, err, out)
	}
}

func (pg *postgres) psqlArgs(extra ...string) []string {
	return append([]string{"-X", "-q", "-At", "-v", "ON_ERROR_STOP=1", "-h", "127.0.0.1", "-p", pg.port, "-U", "postgres", "postgres"}, extra...)
}

func (pg *postgres) exec(t *testing.T, sql string) {
	t.Helper()
	cmd := exec.Command("psql", pg.psqlArgs()...)
	cmd.Env, cmd.Stdin = pg.env, strings.NewReader(sql)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("psql: %v\n%s", err, out)
	}
}

func (pg *postgres) query(t *testing.T, sql string) []string {
	t.Helper()
	cmd := exec.Command("psql", pg.psqlArgs("-c", sql)...)
	cmd.Env = pg.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql %q: %v\n%s", sql, err, out)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// waitForLockWait returns once another session is blocked on a lock, which
// proves the schedule below really interleaves.
func (pg *postgres) waitForLockWait(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		rows := pg.query(t, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type = 'Lock'")
		if len(rows) == 1 && rows[0] == "1" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no session waited on a lock (count %v)", rows)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// psqlSession is one open connection, fed statements in turn.
type psqlSession struct {
	stdin io.WriteCloser
	lines *bufio.Scanner
	cmd   *exec.Cmd
	seq   int
}

func (pg *postgres) session(t *testing.T) *psqlSession {
	t.Helper()
	cmd := exec.Command("psql", pg.psqlArgs()...)
	cmd.Env = pg.env
	cmd.Stderr = os.Stderr // a failed statement's error shows in the test output
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &psqlSession{stdin: stdin, lines: bufio.NewScanner(stdout), cmd: cmd}
	t.Cleanup(func() { stdin.Close(); _ = cmd.Wait() })
	return s
}

type pendingStatement struct {
	s    *psqlSession
	mark string
	done chan error
}

// start sends sql and returns without waiting for it to finish.
func (s *psqlSession) start(t *testing.T, sql string) *pendingStatement {
	t.Helper()
	s.seq++
	mark := fmt.Sprintf("__done_%d__", s.seq)
	if _, err := fmt.Fprintf(s.stdin, "%s\n\\echo %s\n", sql, mark); err != nil {
		t.Fatal(err)
	}
	p := &pendingStatement{s: s, mark: mark, done: make(chan error, 1)}
	go func() {
		for s.lines.Scan() {
			if s.lines.Text() == mark {
				p.done <- nil
				return
			}
		}
		p.done <- fmt.Errorf("psql ended before %s (an error stops the session): %v", mark, s.lines.Err())
	}()
	return p
}

func (p *pendingStatement) wait(t *testing.T) {
	t.Helper()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("statement before %s did not finish", p.mark)
	}
}

// run sends sql and waits for it to finish.
func (s *psqlSession) run(t *testing.T, sql string) {
	t.Helper()
	s.start(t, sql).wait(t)
}
