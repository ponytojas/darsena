package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"filippo.io/age"
	"github.com/ponytojas/darsena/internal/platform/ownerauth"
	rt "github.com/ponytojas/darsena/internal/platform/runtime"
	"github.com/ponytojas/darsena/internal/transport"
)

type config struct {
	command                                                        string
	demo                                                           bool
	experimental                                                   bool
	addr, web, workerSocket, state, socket, configDB, identityFile string
	installation, dockerSocket, intentDB, ownerCredential, origin  string
	identity                                                       *age.X25519Identity
	owner                                                          *ownerauth.Manager
}

// resolveConfig resolves explicit flags over present environment variables over defaults.
// Error messages intentionally never contain supplied values.
func resolveConfig(args []string, lookup func(string) (string, bool)) (config, error) {
	var c config
	if len(args) == 0 || (args[0] != "serve" && args[0] != "worker" && args[0] != "owner-init") {
		return c, errors.New("usage: darsena serve --demo | darsena worker --demo --state <path> --socket <path>")
	}
	c.command = args[0]
	fs := flag.NewFlagSet(c.command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if c.command == "owner-init" {
		fs.StringVar(&c.ownerCredential, "owner-credential", "", "new credential file")
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 || c.ownerCredential == "" {
			return c, errors.New("owner-init requires --owner-credential")
		}
		return c, nil
	}
	fs.BoolVar(&c.demo, "demo", false, "enable demo mode")
	fs.BoolVar(&c.experimental, "experimental", false, "enable experimental local mode")
	fs.StringVar(&c.installation, "installation", "", "stable installation identifier")
	if c.command == "serve" {
		fs.StringVar(&c.addr, "addr", "127.0.0.1:7300", "loopback address")
		fs.StringVar(&c.web, "web", "web/dist", "frontend directory")
		fs.StringVar(&c.workerSocket, "worker-socket", "", "worker socket")
		fs.StringVar(&c.ownerCredential, "owner-credential", "", "owner credential file")
		fs.StringVar(&c.origin, "origin", "", "exact loopback HTTP origin")
	} else {
		fs.StringVar(&c.state, "state", "", "state database")
		fs.StringVar(&c.socket, "socket", "", "worker socket")
		fs.StringVar(&c.configDB, "config-db", "", "configuration database")
		fs.StringVar(&c.identityFile, "age-identity", "", "identity file")
		fs.StringVar(&c.intentDB, "intent-db", "", "runtime intent database")
		fs.StringVar(&c.dockerSocket, "docker-socket", "", "dedicated rootless docker socket")
	}
	// Apply environment after parsing flags so an invalid overridden environment value is harmless.
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return c, flag.ErrHelp
		}
		return c, errors.New("invalid command options; check option names and value types")
	}
	if fs.NArg() != 0 {
		return c, errors.New("unexpected positional argument")
	}
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })
	envs := map[string]string{"demo": "DARSENA_DEMO", "experimental": "DARSENA_EXPERIMENTAL", "installation": "DARSENA_INSTALLATION", "docker-socket": "DARSENA_DOCKER_SOCKET", "intent-db": "DARSENA_INTENT_DB", "owner-credential": "DARSENA_OWNER_CREDENTIAL", "origin": "DARSENA_ORIGIN", "addr": "DARSENA_ADDR", "web": "DARSENA_WEB", "worker-socket": "DARSENA_WORKER_SOCKET", "state": "DARSENA_STATE", "socket": "DARSENA_SOCKET", "config-db": "DARSENA_CONFIG_DB", "age-identity": "DARSENA_AGE_IDENTITY"}
	for name, env := range envs {
		if fs.Lookup(name) == nil || explicit[name] {
			continue
		}
		if v, ok := lookup(env); ok {
			if err := fs.Set(name, v); err != nil {
				return c, fmt.Errorf("invalid value for %s", env)
			}
		}
	}
	if c.demo == c.experimental {
		return c, errors.New("select exactly one of demo and experimental modes")
	}
	if c.experimental && !(rt.Identity{Installation: c.installation, Service: "service", Operation: "initial"}).Valid() {
		return c, errors.New("experimental mode requires a valid installation identifier")
	}
	if c.demo && (c.installation != "" || c.dockerSocket != "" || c.intentDB != "" || c.ownerCredential != "" || c.origin != "") {
		return c, errors.New("experimental options require experimental mode")
	}
	if c.command == "serve" {
		if !transport.LoopbackAddress(c.addr) {
			return c, errors.New("demo mode requires an explicit loopback IP address and port")
		}
		_, port, _ := net.SplitHostPort(c.addr)
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return c, errors.New("listen port must be between 1 and 65535")
		}
		if c.web == "" {
			return c, errors.New("frontend directory must not be empty")
		}
		if c.experimental {
			if c.workerSocket == "" || c.ownerCredential == "" || c.origin != "http://"+c.addr {
				return c, errors.New("experimental server requires worker socket, credential file and exact loopback origin")
			}
			owner, err := ownerauth.New(c.ownerCredential, c.origin)
			if err != nil {
				return c, errors.New("invalid owner credential or origin")
			}
			c.owner = owner
		}
		return c, nil
	}
	if c.state == "" || c.socket == "" {
		return c, errors.New("worker requires --state and --socket")
	}
	if (c.configDB == "") != (c.identityFile == "") {
		return c, errors.New("--config-db and --age-identity must be provided together")
	}
	if c.experimental && (c.configDB == "" || c.intentDB == "" || c.dockerSocket == "") {
		return c, errors.New("experimental worker requires config vault, intent database and docker socket")
	}
	paths := map[string]bool{}
	for _, p := range []string{c.state, c.socket, c.configDB, c.identityFile, c.intentDB, c.dockerSocket} {
		if p == "" {
			continue
		}
		if p == ":memory:" || strings.HasPrefix(strings.ToLower(p), "file:") {
			return c, errors.New("persistent paths must be ordinary filesystem paths")
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return c, errors.New("invalid persistent path")
		}
		if paths[abs] {
			return c, errors.New("state, socket, configuration and identity paths must be distinct")
		}
		paths[abs] = true
	}
	if err := validateWorkerPaths(c); err != nil {
		return c, err
	}
	if c.identityFile != "" {
		identity, err := readIdentity(c.identityFile)
		if err != nil {
			return c, err
		}
		c.identity = identity
	}
	return c, nil
}
func readIdentity(path string) (*age.X25519Identity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("cannot read age identity file")
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("age identity must be a private regular file")
	}
	if info.Size() > 4096 {
		return nil, errors.New("age identity file exceeds 4096 bytes")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("cannot read age identity file")
	}
	identities, err := age.ParseIdentities(strings.NewReader(string(raw)))
	if err != nil || len(identities) != 1 {
		return nil, errors.New("identity file must contain exactly one X25519 identity")
	}
	identity, ok := identities[0].(*age.X25519Identity)
	if !ok {
		return nil, errors.New("identity file must contain exactly one X25519 identity")
	}
	return identity, nil
}

// validateWorkerPaths checks existing filesystem entries without creating anything.
func validateWorkerPaths(c config) error {
	socketDir, err := os.Stat(filepath.Dir(c.socket))
	if err != nil || !socketDir.IsDir() || socketDir.Mode().Perm()&0077 != 0 {
		return errors.New("socket directory must exist and be private (0700)")
	}
	if _, err := os.Lstat(c.socket); err == nil {
		return errors.New("worker socket already exists; verify no worker is running before removing a stale socket")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect worker socket")
	}
	files := []string{c.state, c.state + ".lock", c.state + "-wal", c.state + "-shm"}
	if c.configDB != "" {
		files = append(files, c.configDB, c.configDB+"-wal", c.configDB+"-shm")
	}
	if c.intentDB != "" {
		files = append(files, c.intentDB, c.intentDB+"-wal", c.intentDB+"-shm")
	}
	for _, p := range files {
		if p == "" {
			continue
		}
		dir, err := os.Stat(filepath.Dir(p))
		if err == nil && (!dir.IsDir() || dir.Mode().Perm()&0077 != 0) {
			return errors.New("database directory must be private (0700)")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect database directory")
		}
		info, err := os.Lstat(p)
		if err == nil && (!info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0) {
			return errors.New("database and lock files must be private regular files")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return errors.New("cannot inspect database file")
		}
	}
	// Resolve symlinked parent directories and detect existing hard-link aliases.
	var names []string
	var infos []os.FileInfo
	for _, p := range append(files, c.socket, c.identityFile, c.dockerSocket) {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			return errors.New("invalid persistent path")
		}
		parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
		if err == nil {
			abs = filepath.Join(parent, filepath.Base(abs))
		}
		for _, name := range names {
			if name == abs {
				return errors.New("worker paths and database sidecars must be distinct")
			}
		}
		names = append(names, abs)
		info, err := os.Stat(p)
		if err == nil {
			for _, other := range infos {
				if os.SameFile(info, other) {
					return errors.New("worker paths must not refer to the same file")
				}
			}
			infos = append(infos, info)
		}
	}
	return nil
}
