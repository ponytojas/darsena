package main

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
)

func env(values map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := values[k]; return v, ok }
}
func TestResolvePrecedence(t *testing.T) {
	c, err := resolveConfig([]string{"serve", "--demo", "--addr=127.0.0.1:8001"}, env(map[string]string{"DARSENA_DEMO": "bad-secret", "DARSENA_ADDR": "127.0.0.1:8002", "DARSENA_WEB": "other"}))
	if err != nil || c.addr != "127.0.0.1:8001" || c.web != "other" {
		t.Fatalf("config %#v, err %v", c, err)
	}
	_, err = resolveConfig([]string{"serve", "--demo=false"}, env(map[string]string{"DARSENA_DEMO": "true"}))
	if err == nil {
		t.Fatal("flag must override environment")
	}
	_, err = resolveConfig([]string{"serve"}, env(map[string]string{"DARSENA_DEMO": ""}))
	if err == nil {
		t.Fatal("empty boolean accepted")
	}
}
func TestInvalidInputsNeverEchoValues(t *testing.T) {
	for _, args := range [][]string{{"serve", "--demo=SECRET_MARKER"}, {"serve", "--SECRET_MARKER"}, {"serve", "--demo", "SECRET_MARKER"}} {
		_, err := resolveConfig(args, env(nil))
		if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
			t.Fatalf("unsafe error %v", err)
		}
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err := os.WriteFile(identity, []byte("SECRET_MARKER"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := resolveConfig([]string{"worker", "--demo", "--state", filepath.Join(dir, "state"), "--socket", filepath.Join(dir, "socket"), "--config-db", filepath.Join(dir, "config"), "--age-identity", identity}, env(nil))
	if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
		t.Fatalf("unsafe identity error %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
		t.Fatal("state created during validation")
	}
}
func TestWorkerIdentityAndPathValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err = os.WriteFile(identity, []byte(id.String()), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"worker", "--demo", "--state", filepath.Join(dir, "state"), "--socket", filepath.Join(dir, "socket"), "--config-db", filepath.Join(dir, "config"), "--age-identity", identity}
	c, err := resolveConfig(args, env(nil))
	if err != nil || c.identity == nil {
		t.Fatalf("%v", err)
	}
	args[4] = args[6]
	if _, err = resolveConfig(args, env(nil)); err == nil {
		t.Fatal("duplicate path accepted")
	}
}

func TestWorkerRejectsUnsafePathsBeforeCreatingState(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(dir, "state")
	socket := filepath.Join(dir, "socket")
	args := []string{"worker", "--demo", "--state", state, "--socket", socket}
	if err := os.WriteFile(socket, []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfig(args, env(nil)); err == nil {
		t.Fatal("existing socket accepted")
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("state created on invalid socket")
	}
	if err := os.Remove(socket); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("existing"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfig(args, env(nil)); err == nil {
		t.Fatal("public state accepted")
	}
	if err := os.Remove(state); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "target"), state); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveConfig(args, env(nil)); err == nil {
		t.Fatal("state symlink accepted")
	}
}

func TestDatabaseSidecarsAndSQLiteURI(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity := filepath.Join(dir, "identity")
	if err := os.WriteFile(identity, []byte(id.String()), 0600); err != nil {
		t.Fatal(err)
	}
	state, vault, socket := filepath.Join(dir, "state"), filepath.Join(dir, "vault"), filepath.Join(dir, "socket")
	args := func(s, v, i string) []string {
		return []string{"worker", "--demo", "--state", s, "--socket", socket, "--config-db", v, "--age-identity", i}
	}
	for _, path := range []string{state + "-wal", state + "-shm", vault + "-wal", vault + "-shm"} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if err := os.WriteFile(path, []byte("x"), 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveConfig(args(state, vault, identity), env(nil)); err == nil {
				t.Fatal("public sidecar accepted")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(identity, path); err != nil {
				t.Fatal(err)
			}
			if _, err := resolveConfig(args(state, vault, identity), env(nil)); err == nil {
				t.Fatal("sidecar symlink accepted")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, a := range [][]string{args("file:state", vault, identity), args(state, "file:vault", identity), args(state, vault, vault+"-wal"), args(state, state+"-shm", identity)} {
		if _, err := resolveConfig(a, env(nil)); err == nil {
			t.Fatalf("unsafe paths accepted: %v", a)
		}
	}
}

func TestResolveDefaultsAndInvalidOptions(t *testing.T) {
	c, err := resolveConfig([]string{"serve", "--demo"}, env(nil))
	if err != nil || c.addr != "127.0.0.1:7300" || c.web != "web/dist" {
		t.Fatalf("defaults: %#v %v", c, err)
	}
	c, err = resolveConfig([]string{"serve", "--demo", "--web=first", "--web=last"}, env(map[string]string{"DARSENA_WEB": "env"}))
	if err != nil || c.web != "last" {
		t.Fatalf("repeated flag: %#v %v", c, err)
	}
	for _, args := range [][]string{{"serve", "--demo", "--web="}, {"serve", "--demo", "--state=SECRET_MARKER"}, {"serve", "--demo", "--addr=127.0.0.1:0"}, {"serve", "--demo", "--addr=0.0.0.0:7300"}} {
		_, err := resolveConfig(args, env(nil))
		if err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
			t.Fatalf("unsafe result %v", err)
		}
	}
	if _, err := resolveConfig([]string{"serve", "--help"}, env(nil)); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help: %v", err)
	}
}

func TestIdentityRejectsUnsafeFilesWithoutEcho(t *testing.T) {
	dir := t.TempDir()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, content string
		mode          os.FileMode
	}{
		{"invalid", "SECRET_MARKER", 0600},
		{"public", id.String(), 0644},
		{"large", strings.Repeat("x", 4097), 0600},
		{"multiple", id.String() + "\n" + id.String(), 0600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, tc.name)
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if _, err := readIdentity(path); err == nil || strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Fatalf("identity error: %v", err)
			}
		})
	}
	if _, err := readIdentity(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing identity accepted")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "public"), link); err != nil {
		t.Fatal(err)
	}
	if _, err := readIdentity(link); err == nil {
		t.Fatal("identity symlink accepted")
	}
}

func TestExperimentalModeRequiresExclusiveCompleteConfiguration(t *testing.T) {
	for _, args := range [][]string{
		{"serve", "--demo", "--experimental"},
		{"serve", "--experimental", "--installation", "install"},
		{"worker", "--experimental", "--installation", "install", "--state", "/tmp/state", "--socket", "/tmp/socket"},
		{"serve", "--demo", "--installation", "install"},
	} {
		if _, err := resolveConfig(args, env(nil)); err == nil {
			t.Fatalf("accepted incomplete or mixed mode: %v", args)
		}
	}
}
