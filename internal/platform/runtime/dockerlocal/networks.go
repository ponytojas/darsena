package dockerlocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

const (
	netKindProject = "project"
	netKindShared  = "shared"
	netKindSolo    = "solo"
	iccOption      = "com.docker.network.bridge.enable_icc"
)

// netSpec is the complete definition of a network Dársena owns.
type netSpec struct {
	name, kind, installation, project string
}

func netHash(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:])[:16]
}

// networkSpecs lists the networks a reference needs, the primary one first.
// With a project the primary network is per project; without one it is the
// installation's "solo" network, where neighbours cannot reach each other.
func networkSpecs(r rt.Ref) []netSpec {
	var out []netSpec
	if r.Project != "" {
		out = append(out, netSpec{"darsena-p-" + netHash(r.Identity.Installation, r.Project), netKindProject, r.Identity.Installation, r.Project})
	} else {
		out = append(out, netSpec{"darsena-solo-" + netHash(r.Identity.Installation), netKindSolo, r.Identity.Installation, ""})
	}
	if r.Shared {
		out = append(out, netSpec{"darsena-shared-" + netHash(r.Identity.Installation), netKindShared, r.Identity.Installation, ""})
	}
	return out
}

func expectedNetworks(r rt.Ref) []string {
	specs := networkSpecs(r)
	names := make([]string, len(specs))
	for i, n := range specs {
		names[i] = n.name
	}
	return names
}

func (n netSpec) labels() map[string]string {
	l := map[string]string{prefix + "schema": "1", prefix + "network": n.kind, prefix + "installation": n.installation}
	if n.project != "" {
		l[prefix+"project"] = n.project
	}
	return l
}

func (n netSpec) options() map[string]string {
	if n.kind == netKindSolo {
		return map[string]string{iccOption: "false"}
	}
	return map[string]string{}
}

type networkObserved struct {
	Name       string
	Driver     string
	Scope      string
	Internal   bool
	EnableIPv6 bool
	Attachable bool
	Ingress    bool
	ConfigOnly bool
	Labels     map[string]string
	Options    map[string]string
}

func (o networkObserved) matches(n netSpec) bool {
	if o.Name != n.name || o.Driver != "bridge" || o.Scope != "local" || o.Internal || o.EnableIPv6 || o.Attachable || o.Ingress || o.ConfigOnly {
		return false
	}
	for k, v := range n.labels() {
		if got, ok := o.Labels[k]; !ok || got != v {
			return false
		}
	}
	want := n.options()
	if len(o.Options) != len(want) {
		return false
	}
	for k, v := range want {
		if got, ok := o.Options[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// inspectNetwork returns found=false for an absent network. A network that
// exists but is not exactly what Dársena creates is rt.ErrConflict.
func (a *Adapter) inspectNetwork(ctx context.Context, n netSpec) (bool, error) {
	status, b, err := a.request(ctx, http.MethodGet, api+"/networks/"+url.PathEscape(n.name), nil, false)
	if err != nil {
		return false, err
	}
	if status == 404 {
		return false, nil
	}
	var o networkObserved
	if status != 200 || decode(b, &o) != nil {
		return false, rt.ErrUnavailable
	}
	if !o.matches(n) {
		return true, rt.ErrConflict
	}
	return true, nil
}

// exhausted recognises the daemon's refusal to allocate another subnet.
func exhausted(b []byte) bool {
	var m struct{ Message string }
	if json.Unmarshal(b, &m) != nil {
		return false
	}
	msg := strings.ToLower(m.Message)
	return strings.Contains(msg, "address pool") || strings.Contains(msg, "non-overlapping")
}

// ensureNetwork inspects first and creates only when absent. Ownership labels
// and options are verified before every use; a mismatch fails closed. A create
// race is settled by inspecting again. Networks are never deleted.
func (a *Adapter) ensureNetwork(ctx context.Context, n netSpec) error {
	found, err := a.inspectNetwork(ctx, n)
	if err != nil || found {
		return err
	}
	body := map[string]any{"Name": n.name, "Driver": "bridge", "CheckDuplicate": true, "Internal": false, "EnableIPv6": false, "Attachable": false, "Labels": n.labels(), "Options": n.options()}
	status, b, err := a.request(ctx, http.MethodPost, api+"/networks/create", body, true)
	if err == nil && status != 201 && status != 409 {
		if exhausted(b) {
			return rt.ErrNetworkUnavailable
		}
		if status < 500 {
			return rt.ErrUnavailable
		}
		err = rt.ErrUnknownOutcome
	}
	// Whatever the answer, only what the daemon now holds counts.
	found, ierr := a.inspectNetwork(ctx, n)
	if ierr != nil {
		return ierr
	}
	if !found {
		if err != nil {
			return err
		}
		return rt.ErrUnavailable
	}
	return nil
}

func (a *Adapter) ensureNetworks(ctx context.Context, r rt.Ref) error {
	for _, n := range networkSpecs(r) {
		if err := a.ensureNetwork(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

// EnsureNetworks creates or verifies the networks a specification needs without
// creating a container, so a caller can fail before spending its single
// operation. It is a no-op for isolated specifications. Create ensures them
// again, so this is an early check and not a requirement.
func (a *Adapter) EnsureNetworks(ctx context.Context, s rt.Spec) error {
	if !networked(s.Access) {
		return nil
	}
	r, err := PrepareRef(s)
	if err != nil {
		return err
	}
	return a.ensureNetworks(ctx, r)
}
