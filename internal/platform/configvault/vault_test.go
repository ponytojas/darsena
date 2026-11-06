package configvault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAcceptRevisionSnapshotAndRedaction(t *testing.T) {
	ctx := context.Background()
	id, _ := age.GenerateX25519Identity()
	v, err := Open(ctx, filepath.Join(privateDir(t), "config.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	const marker = "sensitive-marker-87431"
	if _, err := v.Set(ctx, "app", 0, "TOKEN", marker, true); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Set(ctx, "app", 1, "PORT", "8080", false); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Accept(ctx, "app", 0); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := v.Accept(ctx, "missing", 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := v.Accept(ctx, "app", 1); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	accepted, err := v.Accept(ctx, "app", 2)
	if err != nil || accepted.Revision() != 2 {
		t.Fatalf("accept: %v", err)
	}
	// A concurrent edit after acceptance cannot change the accepted values.
	done := make(chan error, 1)
	go func() { _, e := v.Set(ctx, "app", 2, "TOKEN", "new-value", true); done <- e }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	values := make(map[string]string)
	secrets := make(map[string]bool)
	accepted.Visit(func(name, value string, secret bool) { values[name], secrets[name] = value, secret })
	if values["TOKEN"] != marker || values["PORT"] != "8080" || !secrets["TOKEN"] || secrets["PORT"] {
		t.Fatal("snapshot did not retain accepted values")
	}
	if _, err := v.Accept(ctx, "app", 2); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(accepted)
	if err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	slog.New(slog.NewJSONHandler(&log, nil)).Info("snapshot", "accepted", accepted)
	for _, out := range []string{string(encoded), fmt.Sprintf("%v %+v %#v %s", accepted, accepted, accepted, accepted), log.String()} {
		if strings.Contains(out, marker) || strings.Contains(out, "8080") || strings.Contains(out, "TOKEN") {
			t.Fatal("accepted configuration exposed in representation")
		}
	}
}

func TestEncryptedRevisionAndRecovery(t *testing.T) {
	ctx := context.Background()
	dir := privateDir(t)
	path := filepath.Join(dir, "config.db")
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := v.Set(ctx, "app", 0, "PASSWORD", "private-value-xyz", true)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != 1 || len(snap.Variables) != 1 || snap.Variables[0].Value != "" {
		t.Fatal(snap)
	}
	snap, err = v.Set(ctx, "app", 1, "PORT", "8080", false)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Revision != 2 || snap.Variables[1].Value != "8080" {
		t.Fatal(snap)
	}
	if _, err = v.Set(ctx, "app", 1, "PORT", "9999", false); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	v.Close()
	for _, name := range []string{path, path + "-wal"} {
		b, err := os.ReadFile(name)
		if err == nil && bytes.Contains(b, []byte("private-value-xyz")) {
			t.Fatal("secret stored as plaintext")
		}
	}
	other, _ := age.GenerateX25519Identity()
	if bad, err := Open(ctx, path, other); err == nil {
		bad.Close()
		t.Fatal("wrong identity accepted")
	}
	v, err = Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	snap, err = v.Get(ctx, "app")
	if err != nil || snap.Revision != 2 || snap.Variables[0].Value != "" {
		t.Fatal(snap, err)
	}
	snap, err = v.Delete(ctx, "app", 2, "PASSWORD")
	if err != nil || snap.Revision != 3 || len(snap.Variables) != 1 {
		t.Fatal(snap, err)
	}
}
func TestConcurrentWritersAndValidation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(privateDir(t), "config.db")
	id, _ := age.GenerateX25519Identity()
	a, err := Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(ctx, path, id)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if _, err = a.Set(ctx, "app", 0, "BAD-NAME", "x", true); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err = a.Set(ctx, "app", 0, "A", "one", true); err != nil {
		t.Fatal(err)
	}
	if _, err = b.Set(ctx, "app", 0, "B", "two", true); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	snap, err := b.Get(ctx, "app")
	if err != nil || len(snap.Variables) != 1 {
		t.Fatal(snap, err)
	}
}

func TestHistoryIsBounded(t *testing.T) {
	ctx := context.Background()
	id, _ := age.GenerateX25519Identity()
	v, err := Open(ctx, filepath.Join(privateDir(t), "config.db"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	for i := int64(0); i < 25; i++ {
		if _, err = v.Set(ctx, "app", i, "A", "value", true); err != nil {
			t.Fatal(err)
		}
	}
	history, err := v.History(ctx, "app")
	if err != nil || len(history) != 20 || history[0] != 25 || history[19] != 6 {
		t.Fatal(history, err)
	}
}

func TestRefusesInsecureDatabase(t *testing.T) {
	ctx := context.Background()
	root := privateDir(t)
	id, _ := age.GenerateX25519Identity()
	dir := filepath.Join(root, "public")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if v, err := Open(ctx, filepath.Join(dir, "data"), id); err == nil {
		v.Close()
		t.Fatal("accepted public directory")
	}
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if v, err := Open(ctx, link, id); err == nil {
		v.Close()
		t.Fatal("accepted symbolic link")
	}
}

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestCorruptConfigurationErrorsDoNotExposeContents(t *testing.T) {
	ctx := context.Background()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(ctx, filepath.Join(privateDir(t), "vault"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	for _, plaintext := range []string{`{"resource":"app","revision":"SECRET_MARKER","entries":{}}`, `{"resource":"SECRET_MARKER","revision":1,"entries":{}}`} {
		ciphertext, err := v.encrypt([]byte(plaintext))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := v.db.ExecContext(ctx, `INSERT OR REPLACE INTO configurations(resource,revision,ciphertext) VALUES('app',1,?)`, ciphertext); err != nil {
			t.Fatal(err)
		}
		_, err = v.Get(ctx, "app")
		if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
			t.Fatalf("unsafe error: %v", err)
		}
	}
	if _, err := v.db.ExecContext(ctx, `UPDATE configurations SET ciphertext=? WHERE resource='app'`, []byte("SECRET_MARKER")); err != nil {
		t.Fatal(err)
	}
	_, err = v.Get(ctx, "app")
	if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Fatalf("unsafe decrypt error: %v", err)
	}
}

func TestCheckRequiresHistorySchema(t *testing.T) {
	ctx := context.Background()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	v, err := Open(ctx, filepath.Join(privateDir(t), "vault"), id)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := v.db.ExecContext(ctx, "DROP TABLE configuration_history"); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(ctx); err == nil {
		t.Fatal("missing history table reported ready")
	}
}
