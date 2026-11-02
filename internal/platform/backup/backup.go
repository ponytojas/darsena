// Package backup provides offline snapshots of Dársena SQLite stores.
// Callers must stop all writers, including independent vault and intent users.
package backup

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	_ "modernc.org/sqlite"
)

var ErrInvalid = errors.New("invalid or unsafe backup configuration")
var ErrBusy = errors.New("worker state is in use")
var ErrIncomplete = errors.New("backup incomplete or invalid")

type Sources struct{ State, Vault, Intent string }
type entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type manifest struct {
	Version int     `json:"version"`
	Files   []entry `json:"files"`
}

// Create snapshots each store into a new private directory. The manifest is
// written last. A directory without a manifest is incomplete and must not be
// restored. The worker lock does not protect independent database users.
func Create(ctx context.Context, src Sources, destination string) error {
	if src.State == "" {
		return ErrInvalid
	}
	paths := []string{src.State, src.Vault, src.Intent}
	seen := map[string]bool{}
	var inodes []os.FileInfo
	for _, p := range paths {
		if p == "" {
			continue
		}
		a, err := safeFile(p)
		if err != nil || seen[a] {
			return ErrInvalid
		}
		info, err := os.Lstat(a)
		if err != nil {
			return ErrInvalid
		}
		for _, previous := range inodes {
			if os.SameFile(info, previous) {
				return ErrInvalid
			}
		}
		inodes = append(inodes, info)
		seen[a] = true
	}
	parent, err := safeDir(filepath.Dir(destination))
	if err != nil {
		return ErrInvalid
	}
	dest, err := filepath.Abs(destination)
	if err != nil || filepath.Dir(dest) != parent || dest == parent {
		return ErrInvalid
	}
	lock, err := acquire(src.State)
	if err != nil {
		return err
	}
	defer release(lock)
	if err = os.Mkdir(dest, 0700); err != nil {
		return ErrInvalid
	}
	m := manifest{Version: 1}
	for i, name := range []string{"state.db", "vault.db", "intent.db"} {
		if paths[i] == "" {
			continue
		}
		target := filepath.Join(dest, name)
		if err = snapshot(ctx, paths[i], target); err != nil {
			return ErrIncomplete
		}
		if err = checkSnapshot(ctx, target, name); err != nil {
			return ErrIncomplete
		}
		e, eerr := digest(target, name)
		if eerr != nil {
			return ErrIncomplete
		}
		m.Files = append(m.Files, e)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return ErrIncomplete
	}
	f, err := os.OpenFile(filepath.Join(dest, "manifest.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return ErrIncomplete
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if e := f.Close(); err == nil {
		err = e
	}
	if err != nil {
		return ErrIncomplete
	}
	return nil
}

// Restore validates the complete manifest and SQLite snapshots before copying
// into a new private directory; existing data is never overwritten. Checksums
// detect accidental corruption, not malicious modification of the manifest.
func Restore(ctx context.Context, source, destination string) error {
	src, err := safeDir(source)
	if err != nil {
		return ErrInvalid
	}
	parent, err := safeDir(filepath.Dir(destination))
	if err != nil {
		return ErrInvalid
	}
	dest, err := filepath.Abs(destination)
	if err != nil || filepath.Dir(dest) != parent || dest == src {
		return ErrInvalid
	}
	manifestPath := filepath.Join(src, "manifest.json")
	if _, err = safeFile(manifestPath); err != nil {
		return ErrIncomplete
	}
	f, err := os.Open(manifestPath)
	if err != nil {
		return ErrIncomplete
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 16385))
	dec.DisallowUnknownFields()
	var m manifest
	if dec.Decode(&m) != nil || m.Version != 1 || len(m.Files) < 1 || len(m.Files) > 3 {
		return ErrIncomplete
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return ErrIncomplete
	}
	seen := map[string]bool{}
	for _, e := range m.Files {
		if (e.Name != "state.db" && e.Name != "vault.db" && e.Name != "intent.db") || seen[e.Name] {
			return ErrIncomplete
		}
		seen[e.Name] = true
		p := filepath.Join(src, e.Name)
		if _, err = safeFile(p); err != nil {
			return ErrIncomplete
		}
		got, eerr := digest(p, e.Name)
		if eerr != nil || got != e || checkSnapshot(ctx, p, e.Name) != nil {
			return ErrIncomplete
		}
	}
	if !seen["state.db"] {
		return ErrIncomplete
	}
	if err = os.Mkdir(dest, 0700); err != nil {
		return ErrInvalid
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(dest)
		}
	}()
	for _, e := range m.Files {
		target := filepath.Join(dest, e.Name)
		if err = copyFile(filepath.Join(src, e.Name), target); err != nil {
			return ErrIncomplete
		}
		got, verifyErr := digest(target, e.Name)
		if verifyErr != nil || got != e || checkSnapshot(ctx, target, e.Name) != nil {
			return ErrIncomplete
		}
	}
	complete = true
	return nil
}

func snapshot(ctx context.Context, source, target string) error {
	a, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: a}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = db.PingContext(ctx); err != nil {
		return err
	}
	if _, err = db.ExecContext(ctx, "VACUUM INTO ?", target); err != nil {
		return err
	}
	if err = os.Chmod(target, 0600); err != nil {
		return err
	}
	return nil
}
func checkSnapshot(ctx context.Context, p, role string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(p + suffix); !errors.Is(err, os.ErrNotExist) {
			return ErrIncomplete
		}
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: a}
	q := u.Query()
	q.Set("mode", "ro")
	q.Set("immutable", "1")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err = db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil || result != "ok" {
		return ErrIncomplete
	}
	var version int
	if db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version) != nil {
		return ErrIncomplete
	}
	var queries []string
	switch role {
	case "state.db":
		if version != 2 {
			return ErrIncomplete
		}
		queries = []string{"SELECT id,resource_id,kind,idempotency_key,status,error,created_at,updated_at FROM jobs LIMIT 0", "SELECT id,name,image,port,created_at FROM applications LIMIT 0"}
	case "vault.db":
		if version != 0 {
			return ErrIncomplete
		}
		queries = []string{"SELECT name,value FROM metadata LIMIT 0", "SELECT resource,revision,ciphertext FROM configurations LIMIT 0", "SELECT resource,revision,ciphertext FROM configuration_history LIMIT 0"}
		var n int
		if db.QueryRowContext(ctx, "SELECT count(*) FROM metadata WHERE name='identity_check'").Scan(&n) != nil || n != 1 {
			return ErrIncomplete
		}
	case "intent.db":
		if version != 1 && version != 2 {
			return ErrIncomplete
		}
		queries = []string{"SELECT installation,service,operation,resource,fingerprint,image,revision,phase,observation,version FROM runtime_intents LIMIT 0"}
		if version == 2 {
			queries = append(queries, "SELECT installation,service,operation,kind,phase,observation,version FROM runtime_actions LIMIT 0")
		}
	default:
		return ErrIncomplete
	}
	for _, query := range queries {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return ErrIncomplete
		}
		if rows.Close() != nil {
			return ErrIncomplete
		}
	}
	return nil
}
func digest(p, name string) (entry, error) {
	f, err := os.Open(p)
	if err != nil {
		return entry{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return entry{Name: name, Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if e := out.Close(); err == nil {
		err = e
	}
	return err
}
func safeDir(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", ErrInvalid
	}
	resolved, err := filepath.EvalSymlinks(a)
	if err != nil || resolved != a {
		return "", ErrInvalid
	}
	i, err := os.Stat(a)
	if err != nil || !i.IsDir() || i.Mode().Perm()&0077 != 0 {
		return "", ErrInvalid
	}
	return a, nil
}
func safeFile(p string) (string, error) {
	if p == "" || p == ":memory:" || strings.HasPrefix(strings.ToLower(p), "file:") {
		return "", ErrInvalid
	}
	a, err := filepath.Abs(p)
	if err != nil {
		return "", ErrInvalid
	}
	if _, err = safeDir(filepath.Dir(a)); err != nil {
		return "", err
	}
	i, err := os.Lstat(a)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return "", ErrInvalid
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if s, e := os.Lstat(a + suffix); e == nil && (!s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0) {
			return "", ErrInvalid
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return "", ErrInvalid
		}
	}
	return a, nil
}
func acquire(state string) (*os.File, error) {
	fd, err := unix.Open(state+".lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, ErrInvalid
	}
	f := os.NewFile(uintptr(fd), "worker lock")
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, ErrInvalid
	}
	if unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrBusy
	}
	return f, nil
}
func release(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN); _ = f.Close() }
