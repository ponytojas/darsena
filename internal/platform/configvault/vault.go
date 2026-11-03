// Package configvault stores versioned configuration encrypted to an age identity.
// It is an internal building block and does not expose an HTTP interface.
package configvault

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite"
)

var ErrConflict = errors.New("configuration changed; reload before saving")
var ErrInvalid = errors.New("invalid configuration")
var ErrUnavailable = errors.New("configuration unavailable")

// ErrVariableAbsent is an ErrInvalid: the requested variable is not in the revision.
var ErrVariableAbsent = fmt.Errorf("%w: variable not in revision", ErrInvalid)
var ErrNotFound = errors.New("configuration not found")
var ErrNotRetained = errors.New("revision is no longer retained")
var variableName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

const maxValueBytes = 65536
const maxVariables = 200
const maxDocumentBytes = 16 << 20
const retainedRevisions = 20
const schemaVersion = 1

type Variable struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret"`
}
type entry struct {
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
}
type document struct {
	Resource string           `json:"resource"`
	Revision int64            `json:"revision"`
	Entries  map[string]entry `json:"entries"`
	// Touched lists the variable names written or deleted by this revision.
	// It carries no values; it lets history report secret rewrites as changes
	// without comparing secret values. Revisions saved before it existed omit it.
	Touched []string `json:"touched,omitempty"`
}
type Snapshot struct {
	Revision  int64      `json:"revision"`
	Variables []Variable `json:"variables"`
}

// Accepted is an immutable in-memory configuration accepted for a particular
// revision. It deliberately has no serializable or printable value fields.
// It must not be persisted, logged, or retained beyond the operation that
// consumes it. Go strings cannot be reliably erased from memory.
type Accepted struct {
	resource string
	revision int64
	values   map[string]entry
}

func (a Accepted) Revision() int64 { return a.revision }

// Resource identifies the resource to which this accepted revision belongs.
func (a Accepted) Resource() string { return a.resource }

// Visit passes values to an internal runtime boundary. The callback must not
// retain or log them. Visits are sorted and do not expose the backing map.
func (a Accepted) Visit(fn func(name, value string, secret bool)) {
	names := make([]string, 0, len(a.values))
	for name := range a.values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		e := a.values[name]
		fn(name, e.Value, e.Secret)
	}
}

// Format, GoString, MarshalJSON and LogValue prevent accidental disclosure
// through standard formatting, JSON and structured logging.
func (Accepted) Format(f fmt.State, _ rune)   { io.WriteString(f, "[redacted configuration]") }
func (Accepted) GoString() string             { return "[redacted configuration]" }
func (Accepted) MarshalJSON() ([]byte, error) { return []byte(`"[redacted configuration]"`), nil }
func (Accepted) LogValue() slog.Value         { return slog.StringValue("[redacted configuration]") }

type Vault struct {
	db       *sql.DB
	identity *age.X25519Identity
}

// Open uses a separate, private database and an age identity supplied by the owner.
// The caller must protect and back up the identity independently of the database.
func Open(ctx context.Context, path string, identity *age.X25519Identity) (*Vault, error) {
	if identity == nil || path == "" || path == ":memory:" || strings.HasPrefix(strings.ToLower(path), "file:") {
		return nil, fmt.Errorf("persistent path and age identity required")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	dir, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, err
	}
	if !dir.IsDir() || dir.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("configuration directory must be private")
	}
	infoPath, err := os.Lstat(path)
	if err == nil && !infoPath.Mode().IsRegular() {
		return nil, fmt.Errorf("configuration database must be a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		f.Close()
		return nil, fmt.Errorf("configuration database must be a private regular file")
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Vault, error) { db.Close(); return nil, e }
	if _, err = db.ExecContext(ctx, "PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; PRAGMA secure_delete=ON; CREATE TABLE IF NOT EXISTS metadata (name TEXT PRIMARY KEY,value BLOB NOT NULL); CREATE TABLE IF NOT EXISTS configurations (resource TEXT PRIMARY KEY,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL); CREATE TABLE IF NOT EXISTS configuration_history (resource TEXT NOT NULL,revision INTEGER NOT NULL,ciphertext BLOB NOT NULL,PRIMARY KEY(resource,revision));"); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > schemaVersion || version < 0 {
		return fail(fmt.Errorf("unsupported configuration database version %d", version))
	}
	if version == 0 {
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		if _, e = tx.ExecContext(ctx, "ALTER TABLE configuration_history ADD COLUMN created_at TEXT; ALTER TABLE configurations ADD COLUMN updated_at TEXT; PRAGMA user_version=1"); e != nil {
			_ = tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	v := &Vault{db: db, identity: identity}
	var sentinel []byte
	err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE name='identity_check'").Scan(&sentinel)
	if errors.Is(err, sql.ErrNoRows) {
		sentinel, err = v.encrypt([]byte("darsena-config-v1"))
		if err != nil {
			return fail(err)
		}
		if _, err = db.ExecContext(ctx, "INSERT INTO metadata(name,value) VALUES('identity_check',?)", sentinel); err != nil {
			return fail(err)
		}
	} else if err != nil {
		return fail(err)
	}
	plain, err := v.decrypt(sentinel)
	if err != nil || string(plain) != "darsena-config-v1" {
		return fail(fmt.Errorf("configuration identity does not match database"))
	}
	return v, nil
}
func (v *Vault) Close() error { return v.db.Close() }

// Check verifies access to the schema, not the integrity of every encrypted record.
func (v *Vault) Check(ctx context.Context) error {
	var n int
	for _, query := range []string{"SELECT count(*) FROM metadata WHERE 0", "SELECT count(*) FROM configurations WHERE 0", "SELECT count(*) FROM configuration_history WHERE 0", "SELECT count(updated_at) FROM configurations WHERE 0", "SELECT count(created_at) FROM configuration_history WHERE 0"} {
		if err := v.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return err
		}
	}
	return nil
}
func (v *Vault) encrypt(data []byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := age.Encrypt(&out, v.identity.Recipient())
	if err != nil {
		return nil, err
	}
	if _, err = w.Write(data); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
func (v *Vault) decrypt(data []byte) ([]byte, error) {
	reader, err := age.Decrypt(bytes.NewReader(data), v.identity)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(plain) > maxDocumentBytes {
		return nil, fmt.Errorf("configuration exceeds size limit")
	}
	return plain, nil
}
func validResource(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.TrimSpace(s) == s && !strings.ContainsRune(s, 0)
}
func (v *Vault) read(ctx context.Context, resource string) (document, error) {
	if !validResource(resource) {
		return document{}, ErrInvalid
	}
	doc := document{Resource: resource, Entries: map[string]entry{}}
	var ciphertext []byte
	var revision int64
	err := v.db.QueryRowContext(ctx, "SELECT revision,ciphertext FROM configurations WHERE resource=?", resource).Scan(&revision, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return doc, nil
	}
	if err != nil {
		return document{}, err
	}
	raw, err := v.decrypt(ciphertext)
	if err != nil {
		return document{}, errors.New("cannot decrypt configuration")
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		return document{}, errors.New("invalid encrypted configuration")
	}
	if doc.Resource != resource || doc.Revision != revision || doc.Entries == nil {
		return document{}, fmt.Errorf("configuration integrity mismatch")
	}
	return doc, nil
}

// Get never returns secret values. Public values are returned for editing.
func (v *Vault) Get(ctx context.Context, resource string) (Snapshot, error) {
	doc, err := v.read(ctx, resource)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshot(doc), nil
}

// Accept reads the current encrypted document in one database read and accepts
// it only if its revision matches expected. Revision zero means that no saved
// configuration exists and is not deployable. Changes after this read do not
// change the accepted snapshot: SQLite and a runtime do not share a transaction.
// A caller must never retry an ErrConflict with a different revision implicitly.
func (v *Vault) Accept(ctx context.Context, resource string, expected int64) (Accepted, error) {
	if !validResource(resource) || expected <= 0 {
		return Accepted{}, ErrInvalid
	}
	doc, err := v.read(ctx, resource)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Accepted{}, err
		}
		return Accepted{}, ErrUnavailable
	}
	if doc.Revision != expected {
		return Accepted{}, ErrConflict
	}
	if len(doc.Entries) > maxVariables {
		return Accepted{}, ErrUnavailable
	}
	for name, e := range doc.Entries {
		if !variableName.MatchString(name) || len(e.Value) > maxValueBytes || strings.ContainsRune(e.Value, 0) {
			return Accepted{}, ErrUnavailable
		}
	}
	return Accepted{resource: doc.Resource, revision: doc.Revision, values: doc.Entries}, nil
}
func snapshot(doc document) Snapshot {
	out := Snapshot{Revision: doc.Revision, Variables: make([]Variable, 0, len(doc.Entries))}
	for name, e := range doc.Entries {
		x := Variable{Name: name, Secret: e.Secret}
		if !e.Secret {
			x.Value = e.Value
		}
		out.Variables = append(out.Variables, x)
	}
	sort.Slice(out.Variables, func(i, j int) bool { return out.Variables[i].Name < out.Variables[j].Name })
	return out
}

// Set replaces a value without requiring the old value. expected is the last read revision.
func (v *Vault) Set(ctx context.Context, resource string, expected int64, name, value string, secret bool) (Snapshot, error) {
	if !variableName.MatchString(name) || len(value) > maxValueBytes || strings.ContainsRune(value, 0) || expected < 0 {
		return Snapshot{}, ErrInvalid
	}
	doc, err := v.read(ctx, resource)
	if err != nil {
		return Snapshot{}, err
	}
	if doc.Revision != expected {
		return Snapshot{}, ErrConflict
	}
	if _, ok := doc.Entries[name]; !ok && len(doc.Entries) >= maxVariables {
		return Snapshot{}, ErrInvalid
	}
	doc.Entries[name] = entry{value, secret}
	doc.Touched = []string{name}
	return v.save(ctx, doc, expected)
}
func (v *Vault) Delete(ctx context.Context, resource string, expected int64, name string) (Snapshot, error) {
	if !variableName.MatchString(name) || expected < 0 {
		return Snapshot{}, ErrInvalid
	}
	doc, err := v.read(ctx, resource)
	if err != nil {
		return Snapshot{}, err
	}
	if doc.Revision != expected {
		return Snapshot{}, ErrConflict
	}
	if _, ok := doc.Entries[name]; !ok {
		return Snapshot{}, ErrInvalid
	}
	delete(doc.Entries, name)
	doc.Touched = []string{name}
	return v.save(ctx, doc, expected)
}
// Initialize saves an empty first revision for a resource without any saved
// configuration, so an application without variables can be deployed. It
// returns ErrConflict if a configuration already exists.
func (v *Vault) Initialize(ctx context.Context, resource string) (Snapshot, error) {
	doc, err := v.read(ctx, resource)
	if err != nil {
		return Snapshot{}, err
	}
	if doc.Revision != 0 {
		return Snapshot{}, ErrConflict
	}
	doc.Touched = nil
	return v.save(ctx, doc, 0)
}
func (v *Vault) save(ctx context.Context, doc document, expected int64) (Snapshot, error) {
	doc.Revision = expected + 1
	raw, err := json.Marshal(doc)
	if err != nil {
		return Snapshot{}, err
	}
	if len(raw) > maxDocumentBytes {
		return Snapshot{}, ErrInvalid
	}
	ciphertext, err := v.encrypt(raw)
	if err != nil {
		return Snapshot{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return Snapshot{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, "INSERT INTO configurations(resource,revision,ciphertext,updated_at) VALUES(?,?,?,?) ON CONFLICT(resource) DO NOTHING", doc.Resource, doc.Revision, ciphertext, now)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE configurations SET revision=?,ciphertext=?,updated_at=? WHERE resource=? AND revision=?", doc.Revision, ciphertext, now, doc.Resource, expected)
	}
	if err != nil {
		return Snapshot{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, err
	}
	if n != 1 {
		return Snapshot{}, ErrConflict
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO configuration_history(resource,revision,ciphertext,created_at) VALUES(?,?,?,?)", doc.Resource, doc.Revision, ciphertext, now); err != nil {
		return Snapshot{}, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM configuration_history WHERE resource=? AND revision <= ?", doc.Resource, doc.Revision-retainedRevisions); err != nil {
		return Snapshot{}, err
	}
	if err = tx.Commit(); err != nil {
		return Snapshot{}, err
	}
	return snapshot(doc), nil
}

// History lists retained revisions in descending order. It never returns values.
func (v *Vault) History(ctx context.Context, resource string) ([]int64, error) {
	if !validResource(resource) {
		return nil, ErrInvalid
	}
	rows, err := v.db.QueryContext(ctx, "SELECT revision FROM configuration_history WHERE resource=? ORDER BY revision DESC", resource)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// HistoryChange describes one variable that differs from the previous retained
// revision. It never carries a value.
type HistoryChange struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // added, changed or removed
	Secret bool   `json:"secret"`
}

// HistoryEntry is a value-free description of one retained revision.
// Base marks the oldest retained revision: its changes list every variable
// it holds as added, which may not be a real addition if older revisions were pruned.
type HistoryEntry struct {
	Revision  int64           `json:"revision"`
	CreatedAt *time.Time      `json:"created_at"`
	Base      bool            `json:"base"`
	Changes   []HistoryChange `json:"changes"`
}

// RevisionInfo summarizes a stored configuration without decrypting it.
type RevisionInfo struct {
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// RestoreResult reports a completed restore. It deliberately has no value.
type RestoreResult struct {
	Revision int64  `json:"revision"`
	Name     string `json:"name"`
	Secret   bool   `json:"secret"`
}

func parseTime(s sql.NullString) *time.Time {
	if !s.Valid {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, s.String)
	if err != nil {
		return nil
	}
	return &t
}

// readRevision decrypts and validates one retained historical document.
func (v *Vault) readRevision(ctx context.Context, resource string, revision int64) (document, error) {
	var ciphertext []byte
	err := v.db.QueryRowContext(ctx, "SELECT ciphertext FROM configuration_history WHERE resource=? AND revision=?", resource, revision).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return document{}, ErrNotRetained
	}
	if err != nil {
		return document{}, err
	}
	raw, err := v.decrypt(ciphertext)
	if err != nil {
		return document{}, errors.New("cannot decrypt configuration")
	}
	var doc document
	if err = json.Unmarshal(raw, &doc); err != nil {
		return document{}, errors.New("invalid encrypted configuration")
	}
	if doc.Resource != resource || doc.Revision != revision || doc.Entries == nil {
		return document{}, fmt.Errorf("configuration integrity mismatch")
	}
	return doc, nil
}

// HistoryEntries describes retained revisions from oldest to newest without
// returning any value. Only one revision is decrypted at a time besides the
// previous one. A secret variable written in a revision is always "changed",
// even when its value is equal. Revisions saved before writes were recorded
// cannot tell a rewrite from an untouched variable, so there a secret counts
// as changed only if its value differs.
func (v *Vault) HistoryEntries(ctx context.Context, resource string) ([]HistoryEntry, error) {
	if !validResource(resource) {
		return nil, ErrInvalid
	}
	var current int64
	err := v.db.QueryRowContext(ctx, "SELECT revision FROM configurations WHERE resource=?", resource).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	type meta struct {
		revision int64
		created  sql.NullString
	}
	var metas []meta
	rows, err := v.db.QueryContext(ctx, "SELECT revision,created_at FROM configuration_history WHERE resource=? ORDER BY revision ASC", resource)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var m meta
		if err = rows.Scan(&m.revision, &m.created); err != nil {
			rows.Close()
			return nil, err
		}
		metas = append(metas, m)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	out := make([]HistoryEntry, 0, len(metas))
	var prev map[string]entry
	for i, m := range metas {
		doc, err := v.readRevision(ctx, resource, m.revision)
		if err != nil {
			return nil, err
		}
		item := HistoryEntry{Revision: m.revision, CreatedAt: parseTime(m.created), Base: i == 0, Changes: []HistoryChange{}}
		touched := make(map[string]bool, len(doc.Touched))
		for _, n := range doc.Touched {
			touched[n] = true
		}
		for name, e := range doc.Entries {
			p, existed := prev[name]
			switch {
			case !existed:
				item.Changes = append(item.Changes, HistoryChange{name, "added", e.Secret})
			case e.Secret || p.Secret:
				if e.Secret != p.Secret || touched[name] || (doc.Touched == nil && e.Value != p.Value) {
					item.Changes = append(item.Changes, HistoryChange{name, "changed", e.Secret || p.Secret})
				}
			case e.Value != p.Value:
				item.Changes = append(item.Changes, HistoryChange{name, "changed", false})
			}
		}
		for name, p := range prev {
			if _, ok := doc.Entries[name]; !ok {
				item.Changes = append(item.Changes, HistoryChange{name, "removed", p.Secret})
			}
		}
		sort.Slice(item.Changes, func(a, b int) bool { return item.Changes[a].Name < item.Changes[b].Name })
		out = append(out, item)
		prev = doc.Entries
	}
	return out, nil
}

// Restore saves the value and type of a variable from a retained revision as
// revision expected+1. The value never leaves the vault.
func (v *Vault) Restore(ctx context.Context, resource string, expected int64, name string, fromRevision int64) (RestoreResult, error) {
	if !validResource(resource) || !variableName.MatchString(name) || expected < 0 || fromRevision <= 0 {
		return RestoreResult{}, ErrInvalid
	}
	cur, err := v.read(ctx, resource)
	if err != nil {
		return RestoreResult{}, err
	}
	if cur.Revision != expected {
		return RestoreResult{}, ErrConflict
	}
	old, err := v.readRevision(ctx, resource, fromRevision)
	if err != nil {
		return RestoreResult{}, err
	}
	e, ok := old.Entries[name]
	if !ok {
		return RestoreResult{}, ErrVariableAbsent
	}
	snap, err := v.Set(ctx, resource, expected, name, e.Value, e.Secret)
	if err != nil {
		return RestoreResult{}, err
	}
	return RestoreResult{Revision: snap.Revision, Name: name, Secret: e.Secret}, nil
}

// Purge deletes a configuration and its history. It is not a secure erase:
// ciphertext may remain in the WAL, free pages or earlier backups.
func (v *Vault) Purge(ctx context.Context, resource string) error {
	if !validResource(resource) {
		return ErrInvalid
	}
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "DELETE FROM configuration_history WHERE resource=?", resource); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM configurations WHERE resource=?", resource); err != nil {
		return err
	}
	return tx.Commit()
}

// Revisions maps each resource to its current revision and update time in one
// query, without decrypting anything.
func (v *Vault) Revisions(ctx context.Context) (map[string]RevisionInfo, error) {
	rows, err := v.db.QueryContext(ctx, "SELECT resource,revision,updated_at FROM configurations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]RevisionInfo{}
	for rows.Next() {
		var r string
		var info RevisionInfo
		var at sql.NullString
		if err = rows.Scan(&r, &info.Revision, &at); err != nil {
			return nil, err
		}
		info.UpdatedAt = parseTime(at)
		out[r] = info
	}
	return out, rows.Err()
}
