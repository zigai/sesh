//go:build linux || darwin

package remote

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zigai/sesh/internal/privatefs"
	"github.com/zigai/sesh/internal/session"
)

func openTestCatalogDir(t *testing.T) (*privatefs.Dir, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "remote")

	dir, err := openPrivateDir(path, true)
	if err != nil {
		t.Fatalf("openPrivateDir() error = %v", err)
	}

	t.Cleanup(func() {
		if err := dir.Close(); err != nil {
			t.Errorf("closing catalog directory: %v", err)
		}
	})

	return dir, path
}

func testCatalogFixture() Catalog {
	catalog := EmptyCatalog()
	catalog.Connections["host"] = ConnectionState{
		ID:           "host",
		Label:        "Host",
		ConfigDigest: testConfigDigest,
		PeerID:       testPeerID,
		Snapshot:     Snapshot{Health: UnavailableHealth()},
	}

	return catalog
}

func TestWriteCatalogIsPrivateAndLoadable(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
		t.Fatalf("WriteCatalog() error = %v", err)
	}

	info, err := os.Stat(filepath.Join(path, catalogName))
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode = %o, want 600", info.Mode().Perm())
	}

	got, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	if got.Connections["host"].PeerID != testPeerID || got.Version != ProtocolVersion {
		t.Fatalf("loaded catalog = %#v, want the published connection", got)
	}
}

func TestRemoteDirUsesPrivateCacheHome(t *testing.T) {
	cacheHome := filepath.Join(t.TempDir(), "cache")

	path, err := Dir(cacheHome)
	if err != nil {
		t.Fatalf("RemoteDir() error = %v", err)
	}

	want := filepath.Join(cacheHome, "sesh", "remote")
	if path != want {
		t.Fatalf("RemoteDir() = %q, want %q", path, want)
	}

	for _, directory := range []string{cacheHome, filepath.Join(cacheHome, "sesh"), filepath.Join(cacheHome, "sesh", "remote")} {
		info, err := os.Lstat(directory)
		if err != nil {
			t.Fatalf("Lstat(%q) error = %v", directory, err)
		}

		if !info.IsDir() || info.Mode().Perm() != 0o700 {
			t.Fatalf("%q mode = %v, want directory 0700", directory, info.Mode())
		}
	}
}

func TestLoadCatalogMissingIsEmpty(t *testing.T) {
	dir, _ := openTestCatalogDir(t)

	catalog, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	if catalog.Version != ProtocolVersion || len(catalog.Connections) != 0 {
		t.Fatalf("LoadCatalog() = %#v, want empty catalog", catalog)
	}
}

func TestDecodeCatalogNormalizesMissingSnapshotHealth(t *testing.T) {
	catalog := EmptyCatalog()
	catalog.Connections["host"] = ConnectionState{
		ID:           "host",
		ConfigDigest: testConfigDigest,
		Snapshot: Snapshot{Health: SourceHealth{
			Tmux:   SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable},
			Agents: SourceStatus{Status: HealthUnknown, Reason: HealthReasonUnavailable},
		}},
	}

	content, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}

	decoded, err := decodeCatalog(content)
	if err != nil {
		t.Fatalf("decodeCatalog() error = %v", err)
	}

	got := decoded.Connections["host"].Snapshot.Health.Snapshots
	if got.Status != HealthUnknown || got.Reason != HealthReasonUnavailable {
		t.Fatalf("snapshot health = %#v, want unavailable unknown status", got)
	}
}

func TestDecodeCatalogRejectsUnsafeContent(t *testing.T) {
	valid := testCatalogFixture()

	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"unsupported version": []byte(strings.Replace(string(encoded), `"version":`+strconv.Itoa(ProtocolVersion), `"version":1`, 1)),
		"unknown member":      []byte(strings.Replace(string(encoded), `"connections"`, `"forged":1,"connections"`, 1)),
		"duplicate member":    []byte(strings.Replace(string(encoded), `"connections"`, `"connections":null,"connections"`, 1)),
		"trailing content":    append(append([]byte{}, encoded...), []byte(`{}`)...),
		"empty":               {},
		"oversized":           []byte(strings.Repeat("x", maxCatalogBytes+1)),
	}

	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeCatalog(content); err == nil {
				t.Fatalf("decodeCatalog() accepted %s", name)
			}
		})
	}
}

func TestDecodeCatalogRejectsInvalidConnectionState(t *testing.T) {
	tests := map[string]func(*Catalog){
		"mismatched id":      func(c *Catalog) { c.Connections["other"] = c.Connections["host"] },
		"missing digest":     func(c *Catalog) { mutateConnection(c, func(s *ConnectionState) { s.ConfigDigest = "" }) },
		"unknown error code": func(c *Catalog) { mutateConnection(c, func(s *ConnectionState) { s.LastError = "raw failure text" }) },
		"invalid session": func(c *Catalog) {
			mutateConnection(c, func(s *ConnectionState) { s.Snapshot.Sessions = []session.Session{{ID: "", Name: ""}} })
		},
		"invalid health": func(c *Catalog) {
			mutateConnection(c, func(s *ConnectionState) { s.Snapshot.Health.Tmux = SourceStatus{} })
		},
		"control in label": func(c *Catalog) { mutateConnection(c, func(s *ConnectionState) { s.Label = "host\x1b[31m" }) },
		"negative generation": func(c *Catalog) {
			mutateConnection(c, func(s *ConnectionState) {
				s.Sequence = 0
				s.Snapshot.Sessions = []session.Session{{ID: "s", Name: "s", Kind: session.KindLive, WindowCount: -1}}
			})
		},
	}

	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			catalog := testCatalogFixture()
			mutate(&catalog)

			encoded, err := json.Marshal(catalog)
			if err != nil {
				t.Fatal(err)
			}

			if _, err := decodeCatalog(encoded); err == nil {
				t.Fatalf("decodeCatalog() accepted %s", name)
			}
		})
	}
}

func mutateConnection(catalog *Catalog, mutate func(*ConnectionState)) {
	state := catalog.Connections["host"]
	mutate(&state)
	catalog.Connections["host"] = state
}

func TestCatalogRejectsSymlinks(t *testing.T) {
	t.Run("catalog read", testCatalogReadRejectsSymlink)
	t.Run("catalog write", testCatalogWriteRejectsSymlink)
	t.Run("owner lock", testCatalogOwnerLockRejectsSymlink)
	t.Run("directory", testCatalogDirectoryRejectsSymlink)
}

func testCatalogReadRejectsSymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := openPrivateDir(filepath.Join(root, "remote"), true)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = dir.Close() }()

	if err := os.Symlink(target, filepath.Join(root, "remote", catalogName)); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadCatalog(dir); err == nil {
		t.Fatal("LoadCatalog() accepted a symlinked catalog")
	}
}

func testCatalogWriteRejectsSymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target.json")

	prior := []byte("prior catalog")
	if err := os.WriteFile(target, prior, 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := openPrivateDir(filepath.Join(root, "remote"), true)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = dir.Close() }()

	if err := os.Symlink(target, filepath.Join(root, "remote", catalogName)); err != nil {
		t.Fatal(err)
	}

	if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
		t.Fatalf("WriteCatalog() error = %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	if string(content) != string(prior) {
		t.Fatalf("symlink target content = %q, want %q", content, prior)
	}

	loaded, err := LoadCatalog(dir)
	if err != nil {
		t.Fatalf("LoadCatalog() error = %v", err)
	}

	if len(loaded.Connections) != 1 {
		t.Fatalf("loaded catalog = %#v, want the published connection", loaded)
	}
}

func testCatalogOwnerLockRejectsSymlink(t *testing.T) {
	root := t.TempDir()

	lockTarget := filepath.Join(root, "lock-target")
	if err := os.WriteFile(lockTarget, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	dir, err := openPrivateDir(filepath.Join(root, "remote"), true)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = dir.Close() }()

	if err := os.Symlink(lockTarget, filepath.Join(root, "remote", catalogLockName)); err != nil {
		t.Fatal(err)
	}

	lock, err := AcquireCatalogOwner(dir)
	if err == nil {
		_ = lock.Close()

		t.Fatal("AcquireCatalogOwner() accepted a symlinked lock")
	}
}

func testCatalogDirectoryRejectsSymlink(t *testing.T) {
	root := t.TempDir()

	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(root, "cache")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := openPrivateDir(link, true); err == nil {
		t.Fatal("openPrivateDir() accepted a symlinked directory")
	}
}

func TestCatalogOwnerLockIsExclusive(t *testing.T) {
	dir, _ := openTestCatalogDir(t)

	first, err := AcquireCatalogOwner(dir)
	if err != nil {
		t.Fatalf("AcquireCatalogOwner() error = %v", err)
	}

	defer func() { _ = first.Close() }()

	second, err := AcquireCatalogOwner(dir)
	if err == nil {
		_ = second.Close()

		t.Fatal("AcquireCatalogOwner() admitted a second owner")
	}

	if !errors.Is(err, ErrCatalogOwnerRunning) {
		t.Fatalf("AcquireCatalogOwner() error = %v, want %v", err, ErrCatalogOwnerRunning)
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	third, err := AcquireCatalogOwner(dir)
	if err != nil {
		t.Fatalf("AcquireCatalogOwner() after release error = %v", err)
	}

	if err := third.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileCatalogAppliesStaleAndHideLifecycle(t *testing.T) {
	now := time.Unix(1_000_000, 0).UTC()
	catalog := EmptyCatalog()
	catalog.Connections["host"] = ConnectionState{
		ID:           "host",
		ConfigDigest: testConfigDigest,
		Connected:    true,
		LastSeen:     now.Add(-time.Minute),
		Snapshot:     Snapshot{Health: UnavailableHealth()},
	}

	changed, hidden := ReconcileCatalog(&catalog, now, 30*time.Second, 10*time.Minute)
	if !changed || len(hidden) != 0 {
		t.Fatalf("stale reconcile = (%t, %#v), want a visible stale connection", changed, hidden)
	}

	stale := catalog.Connections["host"]
	if stale.Connected || stale.LastError != ConnectionErrorStale || stale.LastErrorAt.IsZero() {
		t.Fatalf("stale connection = %#v, want disconnected with a stale code", stale)
	}

	changed, hidden = ReconcileCatalog(&catalog, now.Add(2*time.Hour), 30*time.Second, 10*time.Minute)
	if !changed || len(hidden) != 1 || hidden["host"].ID != "host" {
		t.Fatalf("hide reconcile = (%t, %#v), want the connection hidden", changed, hidden)
	}

	if _, visible := catalog.Connections["host"]; visible {
		t.Fatal("hidden connection stayed in the visible catalog")
	}
}

func TestCatalogAdmitsBoundsCacheCapacity(t *testing.T) {
	catalog := EmptyCatalog()

	if !CatalogAdmits(catalog, "host", ConnectionState{ID: "host", ConfigDigest: testConfigDigest, Snapshot: Snapshot{Health: UnavailableHealth()}}) {
		t.Fatal("CatalogAdmits() rejected a small connection")
	}

	oversized := ConnectionState{ID: "host", ConfigDigest: testConfigDigest, Snapshot: Snapshot{Health: UnavailableHealth()}}
	oversized.Snapshot.Sessions = make([]session.Session, 0, MaxSessions)

	for index := range MaxSessions {
		oversized.Snapshot.Sessions = append(oversized.Snapshot.Sessions, session.Session{
			ID:                "s" + strconv.Itoa(index),
			Name:              strings.Repeat("n", 256),
			Kind:              session.KindSaved,
			ActivePanePath:    strings.Repeat("p", 4096),
			ActivePaneTitle:   strings.Repeat("t", 4096),
			ActivePaneCommand: strings.Repeat("c", 1024),
			ActiveWindowName:  strings.Repeat("w", 1024),
			ActivePaneID:      strings.Repeat("i", 1024),
			Generation:        strings.Repeat("g", 1024),
			RepositorySource:  strings.Repeat("r", 1024),
		})
	}

	if CatalogAdmits(catalog, "host", oversized) {
		t.Fatal("CatalogAdmits() admitted a connection beyond cache capacity")
	}
}

func TestWatchCatalogYieldsTransitions(t *testing.T) {
	dir, path := openTestCatalogDir(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	type delivery struct {
		catalog Catalog
		err     error
	}

	deliveries := make(chan delivery, 16)

	done := make(chan error, 1)

	go func() {
		done <- WatchCatalog(ctx, dir, minWatchInterval, func(catalog Catalog, err error) error {
			deliveries <- delivery{catalog: catalog, err: err}

			return nil
		})
	}()

	first := <-deliveries
	if first.err != nil || len(first.catalog.Connections) != 0 {
		t.Fatalf("initial delivery = %#v, want an empty catalog", first)
	}

	if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
		t.Fatal(err)
	}

	second := <-deliveries
	if second.err != nil || second.catalog.Connections["host"].PeerID != testPeerID {
		t.Fatalf("second delivery = %#v, want the published connection", second)
	}

	if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(path, catalogName)); err != nil {
		t.Fatal(err)
	}

	third := <-deliveries
	if third.err != nil || len(third.catalog.Connections) != 0 {
		t.Fatalf("third delivery = %#v, want an empty catalog after removal", third)
	}

	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("WatchCatalog() error = %v, want cancellation", err)
	}
}

type catalogDelivery struct {
	catalog Catalog
	err     error
}

func watchTestCatalog(t *testing.T, dir *privatefs.Dir) (func() catalogDelivery, func()) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	deliveries := make(chan catalogDelivery, 16)
	done := make(chan error, 1)

	go func() {
		done <- WatchCatalog(ctx, dir, minWatchInterval, func(catalog Catalog, err error) error {
			deliveries <- catalogDelivery{catalog: catalog, err: err}

			return nil
		})
	}()

	next := func() catalogDelivery {
		t.Helper()

		select {
		case received := <-deliveries:
			return received
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a catalog delivery")

			return catalogDelivery{catalog: Catalog{Version: 0, GeneratedAt: time.Time{}, Connections: nil}, err: nil}
		}
	}

	stop := func() {
		t.Helper()
		cancel()

		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchCatalog() error = %v, want cancellation", err)
		}
	}

	return next, stop
}

func TestWatchCatalogRedeliversUnchangedCatalogAfterReadFailure(t *testing.T) {
	dir, path := openTestCatalogDir(t)
	catalogPath := filepath.Join(path, catalogName)

	if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
		t.Fatal(err)
	}

	next, stop := watchTestCatalog(t, dir)
	defer stop()

	if first := next(); first.err != nil || first.catalog.Connections["host"].PeerID != testPeerID {
		t.Fatalf("initial delivery = %#v, want the published connection", first)
	}

	aside := catalogPath + ".aside"
	if err := errors.Join(os.Rename(catalogPath, aside), os.Mkdir(catalogPath, 0o700)); err != nil {
		t.Fatal(err)
	}

	if failed := next(); failed.err == nil {
		t.Fatalf("delivery = %#v, want the catalog read failure", failed)
	}

	if err := errors.Join(os.Remove(catalogPath), os.Rename(aside, catalogPath)); err != nil {
		t.Fatal(err)
	}

	if recovered := next(); recovered.err != nil || recovered.catalog.Connections["host"].PeerID != testPeerID {
		t.Fatalf("delivery after recovery = %#v, want the unchanged catalog again", recovered)
	}
}

func TestCatalogSessionsProjectsRemoteIdentity(t *testing.T) {
	state := ConnectionState{
		ID:             "host",
		Label:          "Host",
		ConfigDigest:   testConfigDigest,
		PeerID:         testPeerID,
		StreamInstance: "stream",
		Connected:      true,
		Snapshot: Snapshot{
			Health: UnavailableHealth(),
			Sessions: []session.Session{{
				ID: "$1", Name: testSessionName, Kind: session.KindLive,
				TmuxServerSocket: "/remote/tmux.sock",
				Agents:           []session.AgentSession{{CWD: "/work"}},
			}},
		},
	}

	rows := CatalogSessions(state)
	if len(rows) != 1 {
		t.Fatalf("CatalogSessions() = %#v, want one row", rows)
	}

	row := rows[0]
	if row.Origin != session.OriginSSH || row.RemoteStatus != session.RemoteStatusAvailable ||
		row.RuntimeIdentity.ConnectionID != "host" || row.RuntimeIdentity.PeerID != testPeerID ||
		row.RuntimeIdentity.StreamInstance != "stream" || row.RuntimeIdentity.TmuxServerSocket != "/remote/tmux.sock" {
		t.Fatalf("projected row = %#v, want remote identity", row)
	}

	stale := state
	stale.Connected = false
	stale.LastError = ConnectionErrorStale

	if got := CatalogSessions(stale)[0].RemoteStatus; got != session.RemoteStatusStale {
		t.Fatalf("stale status = %q, want %q", got, session.RemoteStatusStale)
	}
}

func TestCatalogConcurrentWritesStayReadable(t *testing.T) {
	dir, _ := openTestCatalogDir(t)

	var group sync.WaitGroup

	writer := make(chan struct{})

	group.Go(func() {
		for range 50 {
			if err := WriteCatalog(dir, testCatalogFixture()); err != nil {
				t.Errorf("WriteCatalog() error = %v", err)

				return
			}
		}

		close(writer)
	})

	group.Go(func() {
		for {
			select {
			case <-writer:
				return
			default:
			}

			catalog, err := LoadCatalog(dir)
			if err != nil {
				t.Errorf("LoadCatalog() error = %v", err)

				return
			}

			if catalog.Version != ProtocolVersion {
				t.Errorf("loaded catalog version = %d, want %d", catalog.Version, ProtocolVersion)

				return
			}
		}
	})

	group.Wait()
}
