package supervisor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lafin716/rhymehub/internal/signing"
)

// stubMain is a stdlib-only stand-in for the rhymehub core. It reads VERSION from the directory
// above its own binary, answers `version`, exits 0 for `migrate`, and serves /healthz on the
// listen address from $RHYMEHUB_HOME/config.json for `serve`.
const stubMain = `package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	exe, err := os.Executable()
	if err != nil {
		fail(err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(exe), "..", "VERSION"))
	if err != nil {
		fail(err)
	}
	version := strings.TrimSpace(string(raw))
	switch os.Args[1] {
	case "version":
		fmt.Println("rhymehub", version)
	case "migrate":
		if stubMode(version) == "migrate-fail" {
			_ = os.WriteFile(filepath.Join(os.Getenv("RHYMEHUB_HOME"), "rhymehub.db"), []byte("partial"), 0o644)
			os.Exit(3)
		}
	case "serve":
		serve(version)
	default:
		os.Exit(2)
	}
}

// stubMode reads the failure mode the test set for this version, if any.
func stubMode(version string) string {
	raw, err := os.ReadFile(filepath.Join(os.Getenv("RHYMEHUB_HOME"), "stub-mode-"+version))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func serve(version string) {
	mode := stubMode(version)
	if mode == "exit" {
		os.Exit(1)
	}
	if mode == "hang" {
		time.Sleep(time.Hour)
	}
	reported := version
	if mode == "wrongversion" {
		reported = "9.9.9"
	}
	raw, err := os.ReadFile(filepath.Join(os.Getenv("RHYMEHUB_HOME"), "config.json"))
	if err != nil {
		fail(err)
	}
	var cfg struct {
		Listen string ` + "`json:\"listen\"`" + `
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		fail(err)
	}
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		fail(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok", "version": reported, "schemaVersion": 1, "draining": false, "uptimeSec": 0,
		})
	})
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, os.Interrupt)
	go func() {
		<-sigs
		os.Exit(0)
	}()
	if err := http.Serve(ln, mux); err != nil {
		fail(err)
	}
}
`

// stubBin is the compiled stub core, built once in TestMain.
var stubBin string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "supervisor-stub-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	stubBin, err = buildStub(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func buildStub(dir string) (string, error) {
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module stubcore\n\ngo 1.21\n"), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(stubMain), 0o644); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "stubcore")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = src
	cmd.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("build stub core: %w: %s", err, b)
	}
	return out, nil
}

// fixture is a temp RHYMEHUB_HOME with 0.1.0 installed as current and a signed feed manifest.
type fixture struct {
	home Home
	pub  ed25519.PublicKey
	priv ed25519.PrivateKey
	stub []byte
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := Home{Root: t.TempDir()}
	if err := h.EnsureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(h.Root, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	stub, err := os.ReadFile(stubBin)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, h.ConfigPath(), fmt.Sprintf("{\"listen\":\"127.0.0.1:%s\"}\n", freePort(t)))

	f := &fixture{home: h, pub: pub, priv: priv, stub: stub}
	if err := installVersion(context.Background(), h, "0.1.0", f.artifact(t, "0.1.0"), 64<<20); err != nil {
		t.Fatalf("install 0.1.0: %v", err)
	}
	if err := os.Symlink(h.VersionDir("0.1.0"), h.Current()); err != nil {
		t.Fatal(err)
	}
	return f
}

// artifact writes a release tarball with the layout installVersion expects and returns its path.
func (f *fixture) artifact(t *testing.T, version string) string {
	t.Helper()
	path := filepath.Join(f.home.Root, "releases", "rhymehub-"+version+".tar.gz")
	writeTarball(t, path, []tarItem{
		{hdr: tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "bin/rhymehub", Typeflag: tar.TypeReg, Mode: 0o755}, body: f.stub},
		{hdr: tar.Header{Name: "VERSION", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte(version + "\n")},
	})
	return path
}

// publish writes releases/manifest.json with one signed artifact for version. mutate, when set,
// edits the artifact before the manifest is written.
func (f *fixture) publish(t *testing.T, version string, mutate func(a *signing.Artifact)) {
	t.Helper()
	f.publishTarball(t, version, f.artifact(t, version), mutate)
}

// publishTarball is publish for an arbitrary tarball path.
func (f *fixture) publishTarball(t *testing.T, version, tb string, mutate func(a *signing.Artifact)) {
	t.Helper()
	digest, _, err := signing.SHA256File(tb)
	if err != nil {
		t.Fatal(err)
	}
	sig, err := signing.SignDigest(f.priv, digest)
	if err != nil {
		t.Fatal(err)
	}
	art := signing.Artifact{
		OS: runtime.GOOS, Arch: runtime.GOARCH,
		URL: filepath.Base(tb), SHA256: digest, Signature: sig,
	}
	if mutate != nil {
		mutate(&art)
	}
	m := &signing.Manifest{Name: "rhymehub", Latest: version}
	m.Upsert(signing.Release{Version: version, PublishedAt: "2026-01-01T00:00:00Z"}, art)
	if err := signing.WriteManifest(filepath.Join(f.home.Root, "releases", "manifest.json"), m); err != nil {
		t.Fatal(err)
	}
}

// newSupervisor builds a Supervisor with short timeouts. Shutdown runs at test cleanup.
func (f *fixture) newSupervisor(t *testing.T, mutate func(o *Options)) *Supervisor {
	t.Helper()
	opts := Options{
		Home:           f.home,
		TrustedKey:     f.pub,
		SupervisorAddr: "127.0.0.1:0",
		DrainTimeout:   2 * time.Second,
		StopTimeout:    3 * time.Second,
		HealthTimeout:  15 * time.Second,
		HealthInterval: 50 * time.Millisecond,
		Logf:           t.Logf,
	}
	if mutate != nil {
		mutate(&opts)
	}
	s, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown() })
	return s
}

type tarItem struct {
	hdr  tar.Header
	body []byte
}

func writeTarball(t *testing.T, path string, items []tarItem) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, it := range items {
		hdr := it.hdr
		if hdr.Typeflag == tar.TypeReg {
			hdr.Size = int64(len(it.body))
		}
		if err := tw.WriteHeader(&hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := tw.Write(it.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// freePort returns a loopback port that was free a moment ago.
func freePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestUpdateAppliesSignedRelease(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	s := f.newSupervisor(t, nil)
	ctx := context.Background()

	rec, err := s.Update(ctx, "0.2.0")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec.Status != StatusSucceeded {
		t.Fatalf("status = %q, want %q", rec.Status, StatusSucceeded)
	}
	if got := currentVersion(f.home); got != "0.2.0" {
		t.Fatalf("current = %q, want 0.2.0", got)
	}
	rep := s.Status(ctx)
	if !rep.CoreHealthy || rep.CoreVersion != "0.2.0" {
		t.Fatalf("status report = %+v, want healthy 0.2.0 core", rep)
	}
}

// setStubMode makes the stub core for version behave as mode says (exit, hang, wrongversion,
// migrate-fail). The stub reads the mode from $RHYMEHUB_HOME/stub-mode-<version>.
func (f *fixture) setStubMode(t *testing.T, version, mode string) {
	t.Helper()
	writeTestFile(t, filepath.Join(f.home.Root, "stub-mode-"+version), mode+"\n")
}

// writeDB puts known bytes in rhymehub.db and its WAL sidecar so restores can be compared exactly.
func (f *fixture) writeDB(t *testing.T) (db, wal []byte) {
	t.Helper()
	db = []byte("original-database-bytes\x00\x01\x02")
	wal = []byte("original-wal-bytes")
	writeTestFile(t, f.home.DBPath(), string(db))
	writeTestFile(t, f.home.DBPath()+"-wal", string(wal))
	return db, wal
}

func assertDB(t *testing.T, h Home, wantDB, wantWAL []byte) {
	t.Helper()
	gotDB, err := os.ReadFile(h.DBPath())
	if err != nil {
		t.Fatalf("read db: %v", err)
	}
	if string(gotDB) != string(wantDB) {
		t.Fatalf("db bytes after restore = %q, want %q", gotDB, wantDB)
	}
	gotWAL, err := os.ReadFile(h.DBPath() + "-wal")
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}
	if string(gotWAL) != string(wantWAL) {
		t.Fatalf("wal bytes after restore = %q, want %q", gotWAL, wantWAL)
	}
}

func assertNoJournal(t *testing.T, h Home) {
	t.Helper()
	if j, err := ReadJournal(h.JournalPath()); err != nil || j != nil {
		t.Fatalf("journal = %+v, %v; want none", j, err)
	}
}

func TestDownloadFailureKeepsOldVersion(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	f.publish(t, "0.2.0", func(a *signing.Artifact) { a.URL = srv.URL + "/rhymehub-0.2.0.tar.gz" })
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded despite a 500 from the artifact server")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	assertNoJournal(t, f.home)
}

// TestExecFailureOnInstallKeepsOldVersion installs a core binary without the execute bit. The
// version check cannot run it, so the update must stop before the old core is touched.
func TestExecFailureOnInstallKeepsOldVersion(t *testing.T) {
	f := newFixture(t)
	tb := filepath.Join(f.home.Root, "releases", "nonexec-0.2.0.tar.gz")
	writeTarball(t, tb, []tarItem{
		{hdr: tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "bin/rhymehub", Typeflag: tar.TypeReg, Mode: 0o644}, body: f.stub},
		{hdr: tar.Header{Name: "VERSION", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("0.2.0\n")},
	})
	f.publishTarball(t, "0.2.0", tb, nil)
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded with a non-executable core binary")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	if _, err := os.Stat(f.home.VersionDir("0.2.0")); !os.IsNotExist(err) {
		t.Fatalf("failed install left %s behind (err %v)", f.home.VersionDir("0.2.0"), err)
	}
}

func TestHealthFailureRollsBack(t *testing.T) {
	cases := []struct {
		name string
		mode string
	}{
		{"core exits immediately", "exit"},
		{"core hangs before listening", "hang"},
		{"core reports wrong version", "wrongversion"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.writeDB(t)
			f.publish(t, "0.2.0", nil)
			f.setStubMode(t, "0.2.0", tc.mode)
			s := f.newSupervisor(t, func(o *Options) { o.HealthTimeout = 2 * time.Second })
			ctx := context.Background()

			rec, err := s.Update(ctx, "0.2.0")
			if err == nil {
				t.Fatal("update succeeded although the new core is unhealthy")
			}
			if rec.Status != StatusRolledBack {
				t.Fatalf("status = %q, want %q (err %v)", rec.Status, StatusRolledBack, err)
			}
			if got := currentVersion(f.home); got != "0.1.0" {
				t.Fatalf("current = %q after rollback, want 0.1.0", got)
			}
			assertNoJournal(t, f.home)
			if rep := s.Status(ctx); !rep.CoreHealthy || rep.CoreVersion != "0.1.0" {
				t.Fatalf("status after rollback = %+v, want healthy 0.1.0 core", rep)
			}
		})
	}
}

// TestMigrationFailureRestoresDBByteForByte makes the new core's migrate step corrupt the DB and
// exit 3. The rollback must put back exactly the bytes that were there before the update.
func TestMigrationFailureRestoresDBByteForByte(t *testing.T) {
	f := newFixture(t)
	wantDB, wantWAL := f.writeDB(t)
	f.publish(t, "0.2.0", nil)
	f.setStubMode(t, "0.2.0", "migrate-fail")
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded although migrate exited 3")
	}
	if rec.Status != StatusRolledBack {
		t.Fatalf("status = %q, want %q", rec.Status, StatusRolledBack)
	}
	if rec.DBBackup == "" {
		t.Fatal("record has no DB backup path")
	}
	assertDB(t, f.home, wantDB, wantWAL)
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	assertNoJournal(t, f.home)
}

// TestRecoverAfterCrashDuringMigration crashes the supervisor after the DB was half-migrated.
// A restarted supervisor must restore the DB to its pre-update bytes and bring back 0.1.0.
func TestRecoverAfterCrashDuringMigration(t *testing.T) {
	f := newFixture(t)
	wantDB, wantWAL := f.writeDB(t)
	f.publish(t, "0.2.0", nil)
	ctx := context.Background()

	crashing := f.newSupervisor(t, func(o *Options) {
		o.PhaseHook = func(phase string) error {
			if phase == string(PhaseMigrating) {
				// Simulate a migration that wrote partial data before the process died.
				writeTestFile(t, f.home.DBPath(), "half-migrated")
				return ErrSimulatedCrash
			}
			return nil
		}
	})
	if _, err := crashing.Update(ctx, "0.2.0"); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("update err = %v, want ErrSimulatedCrash", err)
	}
	if j, err := ReadJournal(f.home.JournalPath()); err != nil || j == nil || j.Phase != string(PhaseMigrating) {
		t.Fatalf("journal = %+v, %v; want a journal at migrating", j, err)
	}

	restarted := f.newSupervisor(t, nil)
	if err := restarted.Recover(ctx); err == nil {
		t.Fatal("Recover returned nil, want the rollback cause")
	}
	assertDB(t, f.home, wantDB, wantWAL)
	assertNoJournal(t, f.home)
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q after recovery, want 0.1.0", got)
	}
	if rep := restarted.Status(ctx); !rep.CoreHealthy || rep.CoreVersion != "0.1.0" {
		t.Fatalf("status after recovery = %+v, want healthy 0.1.0 core", rep)
	}
}

func TestUpdateRefusesBadArtifacts(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(a *signing.Artifact)
		want   error
	}{
		{
			name:   "digest mismatch",
			mutate: func(a *signing.Artifact) { a.SHA256 = strings.Repeat("0", 64) },
			want:   signing.ErrDigestMismatch,
		},
		{
			name: "signed by another key",
			mutate: func(a *signing.Artifact) {
				_, otherPriv, _ := ed25519.GenerateKey(nil)
				sig, _ := signing.SignDigest(otherPriv, a.SHA256)
				a.Signature = sig
			},
			want: signing.ErrBadSignature,
		},
		{
			name:   "unsigned",
			mutate: func(a *signing.Artifact) { a.Signature = "" },
			want:   ErrUnsigned,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.publish(t, "0.2.0", tc.mutate)
			s := f.newSupervisor(t, nil)

			_, err := s.Update(context.Background(), "0.2.0")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := currentVersion(f.home); got != "0.1.0" {
				t.Fatalf("current = %q after refused update, want 0.1.0", got)
			}
		})
	}
}

func TestUnsignedArtifactAcceptedWithAllowUnsigned(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", func(a *signing.Artifact) { a.Signature = "" })
	s := f.newSupervisor(t, func(o *Options) { o.AllowUnsigned = true })

	rec, err := s.Update(context.Background(), "0.2.0")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if rec.Status != StatusSucceeded {
		t.Fatalf("status = %q, want %q", rec.Status, StatusSucceeded)
	}
}

func TestUpdateRejectsCurrentVersion(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	s := f.newSupervisor(t, nil)

	if _, err := s.Update(context.Background(), "0.1.0"); err == nil {
		t.Fatal("update to the current version succeeded, want error")
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
}

func TestManualRollbackReturnsToPreviousVersion(t *testing.T) {
	f := newFixture(t)
	f.writeDB(t)
	f.publish(t, "0.2.0", nil)
	s := f.newSupervisor(t, nil)
	ctx := context.Background()

	if _, err := s.Update(ctx, "0.2.0"); err != nil {
		t.Fatalf("update: %v", err)
	}
	rec, err := s.Rollback(ctx)
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rec.Status != StatusRolledBack {
		t.Fatalf("status = %q, want %q", rec.Status, StatusRolledBack)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
}

// TestRecoverAfterCrashBeforeSwitch simulates a supervisor crash while the update is in the
// switching phase. A fresh supervisor must clear the journal and keep the old version.
func TestRecoverAfterCrashBeforeSwitch(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	ctx := context.Background()

	crashing := f.newSupervisor(t, func(o *Options) {
		o.PhaseHook = func(phase string) error {
			if phase == string(PhaseSwitching) {
				return ErrSimulatedCrash
			}
			return nil
		}
	})
	if _, err := crashing.Update(ctx, "0.2.0"); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("update err = %v, want ErrSimulatedCrash", err)
	}
	if j, err := ReadJournal(f.home.JournalPath()); err != nil || j == nil {
		t.Fatalf("expected a journal after the simulated crash, got %+v, %v", j, err)
	}

	restarted := f.newSupervisor(t, nil)
	// Recover returns the rollback cause as an error even when the rollback succeeds.
	if err := restarted.Recover(ctx); err == nil {
		t.Fatal("Recover returned nil, want the rollback cause")
	}
	if j, err := ReadJournal(f.home.JournalPath()); err != nil || j != nil {
		t.Fatalf("journal after recovery = %+v, %v; want nil", j, err)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q after recovery, want 0.1.0", got)
	}
}

func TestDownloadConnectionRefusedKeepsOldVersion(t *testing.T) {
	f := newFixture(t)
	closed := "http://127.0.0.1:" + freePort(t) + "/rhymehub-0.2.0.tar.gz"
	f.publish(t, "0.2.0", func(a *signing.Artifact) { a.URL = closed })
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded although the artifact server refused the connection")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	assertNoJournal(t, f.home)
}

// TestCorruptBinaryKeepsOldVersion installs a core binary whose bytes are not an executable. The
// install-time version check must reject it before the old core is stopped.
func TestCorruptBinaryKeepsOldVersion(t *testing.T) {
	f := newFixture(t)
	tb := filepath.Join(f.home.Root, "releases", "corrupt-0.2.0.tar.gz")
	writeTarball(t, tb, []tarItem{
		{hdr: tar.Header{Name: "bin/", Typeflag: tar.TypeDir, Mode: 0o755}},
		{hdr: tar.Header{Name: "bin/rhymehub", Typeflag: tar.TypeReg, Mode: 0o755}, body: []byte("\x00garbage, not a binary\xff")},
		{hdr: tar.Header{Name: "VERSION", Typeflag: tar.TypeReg, Mode: 0o644}, body: []byte("0.2.0\n")},
	})
	f.publishTarball(t, "0.2.0", tb, nil)
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded with a corrupt core binary")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	if _, err := os.Stat(f.home.VersionDir("0.2.0")); !os.IsNotExist(err) {
		t.Fatalf("corrupt install left %s behind (err %v)", f.home.VersionDir("0.2.0"), err)
	}
}

// TestRecoverAfterCrashDuringHealthcheck crashes after the new core was started and before it was
// checked. The new core is still running, as it would be after a real crash. Run kills stale cores
// before Recover, so the restarted supervisor sees the new version healthy and commits it.
func TestRecoverAfterCrashDuringHealthcheck(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	ctx := context.Background()

	crashing := f.newSupervisor(t, func(o *Options) {
		o.PhaseHook = func(phase string) error {
			if phase == string(PhaseHealthcheck) {
				return ErrSimulatedCrash
			}
			return nil
		}
	})
	if _, err := crashing.Update(ctx, "0.2.0"); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("update err = %v, want ErrSimulatedCrash", err)
	}
	if j, err := ReadJournal(f.home.JournalPath()); err != nil || j == nil || j.Phase != string(PhaseHealthcheck) {
		t.Fatalf("journal = %+v, %v; want a journal at healthcheck", j, err)
	}

	if err := killStaleCore(f.home); err != nil {
		t.Fatalf("kill stale core: %v", err)
	}
	restarted := f.newSupervisor(t, nil)
	if err := restarted.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	assertNoJournal(t, f.home)
	if got := currentVersion(f.home); got != "0.2.0" {
		t.Fatalf("current = %q after recovery, want 0.2.0", got)
	}
	if rep := restarted.Status(ctx); !rep.CoreHealthy || rep.CoreVersion != "0.2.0" {
		t.Fatalf("status after recovery = %+v, want healthy 0.2.0 core", rep)
	}
}

// TestNoBackupRollbackKeepsDBAndNeedsManualRecovery covers a first update, where no database existed
// and so no backup was taken. A failed migrate must not delete the database. The record is failed
// and says a manual recovery is needed.
func TestNoBackupRollbackKeepsDBAndNeedsManualRecovery(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	f.setStubMode(t, "0.2.0", "migrate-fail")
	s := f.newSupervisor(t, nil)

	rec, err := s.Update(context.Background(), "0.2.0")
	if err == nil {
		t.Fatal("update succeeded although migrate exited 3")
	}
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if rec.DBBackup != "" {
		t.Fatalf("record has a DB backup %q, want none", rec.DBBackup)
	}
	if !strings.Contains(rec.Error, "db backup missing; manual recovery required") {
		t.Fatalf("record error = %q, want it to say db backup missing; manual recovery required", rec.Error)
	}
	got, err := os.ReadFile(f.home.DBPath())
	if err != nil {
		t.Fatalf("database was removed: %v", err)
	}
	if string(got) != "partial" {
		t.Fatalf("database = %q, want it left as migrate wrote it", got)
	}
	if got := currentVersion(f.home); got != "0.1.0" {
		t.Fatalf("current = %q, want 0.1.0", got)
	}
	assertNoJournal(t, f.home)
}

// haltedSupervisor runs 0.1.0 as the live core, then fails an update to 0.2.0 at migrate with no DB
// backup. The rollback has to halt. It must not start 0.1.0 again on the database.
func haltedSupervisor(t *testing.T, f *fixture) (*Supervisor, UpdateRecord) {
	t.Helper()
	s := f.newSupervisor(t, nil)
	ctx := context.Background()
	if err := s.EnsureCore(ctx); err != nil {
		t.Fatalf("start 0.1.0 core: %v", err)
	}
	f.publish(t, "0.2.0", nil)
	f.setStubMode(t, "0.2.0", "migrate-fail")
	rec, err := s.Update(ctx, "0.2.0")
	if err == nil {
		t.Fatal("update succeeded although migrate exited 3")
	}
	return s, rec
}

// TestNoBackupRollbackDoesNotRestartOldCore checks that a rollback with no DB backup leaves no core
// running, records the halt, and refuses EnsureCore.
func TestNoBackupRollbackDoesNotRestartOldCore(t *testing.T) {
	f := newFixture(t)
	s, rec := haltedSupervisor(t, f)
	ctx := context.Background()
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if !strings.Contains(rec.Error, noBackupReason) {
		t.Fatalf("record error = %q, want it to contain %q", rec.Error, noBackupReason)
	}
	if p := s.currentCore(); p != nil {
		t.Fatalf("core pid %d was started after a no-backup rollback, want none", p.pid)
	}
	rep := s.Status(ctx)
	if rep.CoreRunning || rep.CoreHealthy {
		t.Fatalf("status = %+v, want no core running", rep)
	}
	if rep.HaltedReason != noBackupReason {
		t.Fatalf("halted reason = %q, want %q", rep.HaltedReason, noBackupReason)
	}
	if err := s.EnsureCore(ctx); !errors.Is(err, errHalted) {
		t.Fatalf("EnsureCore err = %v, want errHalted", err)
	}
	if p := s.currentCore(); p != nil {
		t.Fatalf("EnsureCore started core pid %d while halted", p.pid)
	}
}

// TestRunLoopDoesNotRelaunchWhileHalted checks that runLoop starts a core only once the halt is
// cleared. The clear is the control: without it the same loop would start the core.
func TestRunLoopDoesNotRelaunchWhileHalted(t *testing.T) {
	f := newFixture(t)
	s := f.newSupervisor(t, nil)
	if err := s.halt("test halt"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.runLoop(ctx)
		close(done)
	}()
	defer func() {
		cancel()
		<-done
	}()

	time.Sleep(time.Second)
	if p := s.currentCore(); p != nil {
		t.Fatalf("runLoop started core pid %d while halted", p.pid)
	}

	if err := s.saveState(supervisorState{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for s.currentCore() == nil {
		if time.Now().After(deadline) {
			t.Fatal("runLoop did not start the core after the halt was cleared")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestHaltSurvivesRestart checks that a halt written before a restart is loaded by a new supervisor.
func TestHaltSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	s, rec := haltedSupervisor(t, f)
	if rec.Status != StatusFailed {
		t.Fatalf("status = %q, want %q", rec.Status, StatusFailed)
	}
	if got := readState(f.home).HaltedReason; got != noBackupReason {
		t.Fatalf("persisted halt = %q, want %q", got, noBackupReason)
	}

	ctx := context.Background()
	restarted := f.newSupervisor(t, nil)
	if got := restarted.Status(ctx).HaltedReason; got != noBackupReason {
		t.Fatalf("restarted status halt = %q, want %q", got, noBackupReason)
	}
	if err := restarted.EnsureCore(ctx); !errors.Is(err, errHalted) {
		t.Fatalf("restarted EnsureCore err = %v, want errHalted", err)
	}
	if p := restarted.currentCore(); p != nil {
		t.Fatalf("restarted supervisor started core pid %d while halted", p.pid)
	}
	_ = s
}

// TestRollbackWithoutDBBackupIsRefused checks that a manual rollback to a version reachable only by
// restoring a missing DB backup is refused. The running core, the state, and any existing halt are
// left alone, and the API answers 409.
func TestRollbackWithoutDBBackupIsRefused(t *testing.T) {
	f := newFixture(t)
	f.publish(t, "0.2.0", nil)
	s := f.newSupervisor(t, nil)
	ctx := context.Background()
	// No DB exists, so the update takes no backup, but migrate still ran on success.
	if _, err := s.Update(ctx, "0.2.0"); err != nil {
		t.Fatalf("update: %v", err)
	}
	before := s.currentCore()
	if before == nil {
		t.Fatal("no core running after successful update")
	}
	if err := s.halt(noBackupReason); err != nil {
		t.Fatal(err)
	}

	rec, err := s.Rollback(ctx)
	if !errors.Is(err, ErrNoBackupRollback) {
		t.Fatalf("rollback err = %v, want ErrNoBackupRollback", err)
	}
	if rec.ID != "" {
		t.Fatalf("refused rollback recorded history entry %+v, want none", rec)
	}
	if got := s.currentCore(); got != before {
		t.Fatalf("core changed during refused rollback: before %v, after %v", before, got)
	}
	if got := currentVersion(f.home); got != "0.2.0" {
		t.Fatalf("current = %q, want 0.2.0", got)
	}
	st := readState(f.home)
	if st.Previous != "0.1.0" {
		t.Fatalf("previous = %q, want 0.1.0 kept", st.Previous)
	}
	if st.HaltedReason != noBackupReason {
		t.Fatalf("halt = %q, want %q kept", st.HaltedReason, noBackupReason)
	}
	assertNoJournal(t, f.home)

	rr := postAPI(s, "/rollback", "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("API code = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), noBackupReason) {
		t.Fatalf("API body = %s, want it to contain %q", rr.Body.String(), noBackupReason)
	}
}

// TestSuccessfulUpdateClearsHalt checks that an explicit successful update clears the halt, both in
// memory and on disk.
func TestSuccessfulUpdateClearsHalt(t *testing.T) {
	f := newFixture(t)
	s, _ := haltedSupervisor(t, f)
	ctx := context.Background()

	f.publish(t, "0.3.0", nil)
	rec, err := s.Update(ctx, "0.3.0")
	if err != nil {
		t.Fatalf("update after halt: %v", err)
	}
	if rec.Status != StatusSucceeded {
		t.Fatalf("status = %q, want %q", rec.Status, StatusSucceeded)
	}
	if got := readState(f.home).HaltedReason; got != "" {
		t.Fatalf("persisted halt = %q after successful update, want none", got)
	}
	if got := s.Status(ctx).HaltedReason; got != "" {
		t.Fatalf("status halt = %q after successful update, want none", got)
	}
	restarted := f.newSupervisor(t, nil)
	if got := restarted.Status(ctx).HaltedReason; got != "" {
		t.Fatalf("restarted status halt = %q, want none", got)
	}
}

// TestManualRollbackWritesJournal crashes a manual rollback at the switching phase. The journal must
// be on disk, and a restarted supervisor must record the rollback as failed and clear the journal.
func TestManualRollbackWritesJournal(t *testing.T) {
	f := newFixture(t)
	f.writeDB(t)
	f.publish(t, "0.2.0", nil)
	ctx := context.Background()
	s := f.newSupervisor(t, nil)
	if _, err := s.Update(ctx, "0.2.0"); err != nil {
		t.Fatalf("update: %v", err)
	}

	s.opts.PhaseHook = func(phase string) error {
		if phase == PhaseSwitching {
			return ErrSimulatedCrash
		}
		return nil
	}
	if _, err := s.Rollback(ctx); !errors.Is(err, ErrSimulatedCrash) {
		t.Fatalf("rollback err = %v, want ErrSimulatedCrash", err)
	}
	if j, err := ReadJournal(f.home.JournalPath()); err != nil || j == nil || !j.Manual || j.Phase != PhaseSwitching {
		t.Fatalf("journal = %+v, %v; want a manual journal at switching", j, err)
	}

	restarted := f.newSupervisor(t, nil)
	if err := restarted.Recover(ctx); err == nil {
		t.Fatal("Recover returned nil for an interrupted manual rollback, want an error")
	}
	assertNoJournal(t, f.home)
	history, err := ReadHistory(f.home.UpdatesLogPath())
	if err != nil || len(history) == 0 {
		t.Fatalf("history = %+v, %v", history, err)
	}
	if last := history[len(history)-1]; last.Status != StatusFailed {
		t.Fatalf("last history status = %q, want %q", last.Status, StatusFailed)
	}
}

// postAPI sends an authorized POST to the supervisor handler.
func postAPI(s *Supervisor, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+s.Token())
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

// TestAPIOutcomeStatusCodes checks the codes for update and rollback outcomes. A failed update is
// 500 and a rolled-back one is 409. Both carry the record in the body. A successful rollback is 200.
func TestAPIOutcomeStatusCodes(t *testing.T) {
	t.Run("download failure is 500 with failed record", func(t *testing.T) {
		f := newFixture(t)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		f.publish(t, "0.2.0", func(a *signing.Artifact) { a.URL = srv.URL + "/rhymehub-0.2.0.tar.gz" })
		s := f.newSupervisor(t, nil)

		rr := postAPI(s, "/update", `{"version":"0.2.0"}`)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("code = %d, want 500; body: %s", rr.Code, rr.Body.String())
		}
		var rec UpdateRecord
		if err := json.Unmarshal(rr.Body.Bytes(), &rec); err != nil {
			t.Fatalf("body is not a record: %v (%s)", err, rr.Body.String())
		}
		if rec.ID == "" || rec.Status != StatusFailed || rec.Error == "" {
			t.Fatalf("record = %+v, want an id, status failed and an error", rec)
		}
	})

	t.Run("health failure is 409 with rolled_back record", func(t *testing.T) {
		f := newFixture(t)
		f.writeDB(t)
		f.publish(t, "0.2.0", nil)
		f.setStubMode(t, "0.2.0", "exit")
		s := f.newSupervisor(t, func(o *Options) { o.HealthTimeout = 2 * time.Second })

		rr := postAPI(s, "/update", `{"version":"0.2.0"}`)
		if rr.Code != http.StatusConflict {
			t.Fatalf("code = %d, want 409; body: %s", rr.Code, rr.Body.String())
		}
		var rec UpdateRecord
		if err := json.Unmarshal(rr.Body.Bytes(), &rec); err != nil {
			t.Fatalf("body is not a record: %v (%s)", err, rr.Body.String())
		}
		if rec.Status != StatusRolledBack {
			t.Fatalf("status = %q, want %q", rec.Status, StatusRolledBack)
		}
	})

	t.Run("successful rollback is 200", func(t *testing.T) {
		f := newFixture(t)
		f.writeDB(t)
		f.publish(t, "0.2.0", nil)
		s := f.newSupervisor(t, nil)
		if _, err := s.Update(context.Background(), "0.2.0"); err != nil {
			t.Fatalf("update: %v", err)
		}

		rr := postAPI(s, "/rollback", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("code = %d, want 200; body: %s", rr.Code, rr.Body.String())
		}
		if got := currentVersion(f.home); got != "0.1.0" {
			t.Fatalf("current = %q, want 0.1.0", got)
		}
	})
}

func TestHandlerRequiresBearerAndMethod(t *testing.T) {
	f := newFixture(t)
	s := f.newSupervisor(t, nil)
	h := s.Handler()

	cases := []struct {
		name   string
		method string
		path   string
		auth   string
		body   string
		want   int
	}{
		{"no auth", http.MethodGet, "/status", "", "", http.StatusUnauthorized},
		{"wrong token", http.MethodGet, "/status", "Bearer nope", "", http.StatusUnauthorized},
		{"wrong method", http.MethodGet, "/update", "Bearer " + s.Token(), "", http.StatusMethodNotAllowed},
		{"empty update body", http.MethodPost, "/update", "Bearer " + s.Token(), "", http.StatusBadRequest},
		{"status ok", http.MethodGet, "/status", "Bearer " + s.Token(), "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.auth != "" {
				req.Header.Set("Authorization", tc.auth)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("code = %d, want %d; body: %s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

// tar entries for safeExtract tests.
func TestSafeExtractRejectsLinksAndTraversal(t *testing.T) {
	cases := map[string]tarItem{
		"symlink": {hdr: tar.Header{Name: "bin/rhymehub", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}},
		"traversal": {
			hdr:  tar.Header{Name: "../evil", Typeflag: tar.TypeReg, Mode: 0o755},
			body: []byte("x"),
		},
		"absolute": {
			hdr:  tar.Header{Name: "/tmp/evil", Typeflag: tar.TypeReg, Mode: 0o755},
			body: []byte("x"),
		},
	}
	for name, item := range cases {
		t.Run(name, func(t *testing.T) {
			tb := filepath.Join(t.TempDir(), "bad.tar.gz")
			writeTarball(t, tb, []tarItem{item})
			if err := safeExtract(tb, t.TempDir(), 1<<20); err == nil {
				t.Fatal("safeExtract accepted a disallowed entry")
			}
		})
	}
}

func TestRenderLaunchdPlistEscapesValues(t *testing.T) {
	h := Home{Root: "/tmp/rh&<x>"}
	data, err := RenderLaunchdPlist(LaunchdOptions{Home: h, Binary: "/opt/a&b/rhymehub-supervisor"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{"&amp;", "&lt;", "&gt;"} {
		if !strings.Contains(s, want) {
			t.Fatalf("plist missing %q", want)
		}
	}
	if strings.Contains(s, "&<") {
		t.Fatal("plist contains an unescaped ampersand followed by '<'")
	}
	if _, err := RenderLaunchdPlist(LaunchdOptions{Home: h, Binary: "relative/bin"}); err == nil {
		t.Fatal("relative binary path accepted")
	}
}

// TestRenderLaunchdPlistIsWellFormedXML decodes the whole rendered plist with a strict XML decoder,
// the same check plutil -lint performs on the document structure.
func TestRenderLaunchdPlistIsWellFormedXML(t *testing.T) {
	home := "/tmp/rh&<x>"
	bin := "/opt/a&b/rhymehub-supervisor"
	data, err := RenderLaunchdPlist(LaunchdOptions{Home: Home{Root: home}, Binary: bin})
	if err != nil {
		t.Fatal(err)
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = true
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("plist is not well-formed XML: %v", err)
		}
		if cd, ok := tok.(xml.CharData); ok {
			text.Write(cd)
			text.WriteByte('\n')
		}
	}
	for _, want := range []string{home, bin, LaunchdLabel} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("decoded plist text does not contain %q", want)
		}
	}
}

func TestInstallLaunchdWritesPlistWithoutLoading(t *testing.T) {
	h := Home{Root: t.TempDir()}
	dest := filepath.Join(t.TempDir(), "agents", LaunchdLabel+".plist")
	path, err := InstallLaunchd(LaunchdOptions{Home: h, Binary: "/opt/rhymehub/rhymehub-supervisor", Dest: dest})
	if err != nil {
		t.Fatal(err)
	}
	if path != dest {
		t.Fatalf("path = %q, want %q", path, dest)
	}
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("plist mode = %v, want 0644", info.Mode().Perm())
	}
}
