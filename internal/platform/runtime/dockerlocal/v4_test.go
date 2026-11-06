package dockerlocal

import (
	"context"
	"errors"
	"strings"
	"testing"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

// v3Fingerprint was computed from the policy-v3 code before policy v4 existed.
// If it ever changes, every existing isolated service stops being recognised.
const v3Fingerprint = "2c1732f145ca44aeb133cccd9f890061a16c716eabfe9b95f27c56f1f9dd52a8"

func specV4(access string) rt.Spec {
	s := spec()
	s.Access, s.Port, s.Project, s.Alias = access, 80, "proj-1", "web"
	return s
}

func engineV4(user string) *engine {
	e := fake()
	e.imageUser = user
	return e
}

func hostOf(e *engine) map[string]any { return e.container["HostConfig"].(map[string]any) }
func netsOf(e *engine) map[string]any {
	return e.container["NetworkSettings"].(map[string]any)["Networks"].(map[string]any)
}

func TestV3FingerprintPinnedAndIsolatedAccessIsIdentical(t *testing.T) {
	if got := fingerprint(spec()); got != v3Fingerprint {
		t.Fatalf("v3 fingerprint changed: %s", got)
	}
	s := spec()
	s.Access = rt.AccessIsolated
	if fingerprint(s) != v3Fingerprint {
		t.Fatal("explicit isolated differs from empty access")
	}
	if fingerprint(specV4(rt.AccessInternal)) == v3Fingerprint || fingerprint(specV4(rt.AccessInternal)) == fingerprint(specV4(rt.AccessLocal)) {
		t.Fatal("v4 must not share the v3 fingerprint nor ignore access")
	}
	for name, mutate := range map[string]func(*rt.Spec){
		"port":    func(s *rt.Spec) { s.Port = 81 },
		"project": func(s *rt.Spec) { s.Project = "proj-2" },
		"shared":  func(s *rt.Spec) { s.Shared = true },
		"alias":   func(s *rt.Spec) { s.Alias = "other" },
	} {
		s := specV4(rt.AccessLocal)
		mutate(&s)
		if fingerprint(s) == fingerprint(specV4(rt.AccessLocal)) {
			t.Fatalf("%s not bound by fingerprint", name)
		}
	}
}

func TestV3ContainerStillAcceptedWithIsolatedAccess(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	s := spec()
	s.Access = rt.AccessIsolated
	res, err := a.Create(context.Background(), s)
	if err != nil || res.State != rt.StateRunning || res.HostPort != 0 || res.Ref.Fingerprint != v3Fingerprint {
		t.Fatalf("%+v %v", res, err)
	}
	if e.netPosts != 0 || e.lastCreate["User"] != "65532:65532" {
		t.Fatal("isolated service touched networks or lost its user")
	}
	// A reference without access (as stored before v4) inspects the same container.
	old := res.Ref
	old.Access = ""
	if got, err := a.Inspect(context.Background(), old); err != nil || got.State != rt.StateRunning {
		t.Fatalf("legacy ref: %v %v", got, err)
	}
}

func TestSpecValidationOfAccessCombinations(t *testing.T) {
	good := []rt.Spec{specV4(rt.AccessLocal), specV4(rt.AccessInternal), spec()}
	internalNoPort := specV4(rt.AccessInternal)
	internalNoPort.Port = 0
	good = append(good, internalNoPort)
	for _, s := range good {
		if s.Validate() != nil {
			t.Fatalf("rejected %+v", s)
		}
	}
	for name, mutate := range map[string]func(*rt.Spec){
		"port on isolated":       func(s *rt.Spec) { *s = spec(); s.Port = 80 },
		"alias on isolated":      func(s *rt.Spec) { *s = spec(); s.Alias = "web" },
		"shared on isolated":     func(s *rt.Spec) { *s = spec(); s.Access = rt.AccessIsolated; s.Shared = true },
		"project on isolated":    func(s *rt.Spec) { *s = spec(); s.Project = "p" },
		"local without port":     func(s *rt.Spec) { s.Port = 0 },
		"port out of range":      func(s *rt.Spec) { s.Port = 70000 },
		"negative port":          func(s *rt.Spec) { s.Port = -1 },
		"unknown access":         func(s *rt.Spec) { s.Access = "public" },
		"no alias":               func(s *rt.Spec) { s.Alias = "" },
		"uppercase alias":        func(s *rt.Spec) { s.Alias = "Web" },
		"invalid project":        func(s *rt.Spec) { s.Project = "a b" },
		"internal negative port": func(s *rt.Spec) { s.Access = rt.AccessInternal; s.Port = -5 },
	} {
		s := specV4(rt.AccessLocal)
		mutate(&s)
		if s.Validate() == nil {
			t.Fatalf("%s accepted", name)
		}
		if _, err := newAdapter(fake()).Create(context.Background(), s); !errors.Is(err, rt.ErrInvalid) {
			t.Fatalf("%s: create %v", name, err)
		}
	}
}

func TestV4LocalRootImageLifecycle(t *testing.T) {
	e := engineV4("")
	e.exposed = []string{"80/tcp"}
	a := newAdapter(e)
	ctx := context.Background()
	s := specV4(rt.AccessLocal)
	s.Shared = true
	res, err := a.Create(ctx, s)
	if err != nil || res.State != rt.StateRunning || res.HostPort != 32768 {
		t.Fatalf("%+v %v", res, err)
	}
	if _, set := e.lastCreate["User"]; set {
		t.Fatal("v4 must not force a user")
	}
	host := hostOf(e)
	if host["ReadonlyRootfs"] != false || len(host["CapAdd"].([]any)) != 3 {
		t.Fatalf("host config: %v", host)
	}
	for _, c := range host["CapAdd"].([]any) {
		if c != "CAP_CHOWN" && c != "CAP_SETGID" && c != "CAP_SETUID" {
			t.Fatalf("capability %v", c)
		}
	}
	if len(netsOf(e)) != 2 || e.netPosts != 2 {
		t.Fatalf("networks %v created %d", netsOf(e), e.netPosts)
	}
	if res.Ref.Access != rt.AccessLocal || res.Ref.Port != 80 || res.Ref.Alias != "web" || !res.Ref.Shared || res.Ref.Project != "proj-1" {
		t.Fatalf("ref %+v", res.Ref)
	}
	again, err := a.Create(ctx, s)
	if err != nil || again != res || e.posts != 1 || e.netPosts != 2 {
		t.Fatalf("repeat %+v %v", again, err)
	}
	stopped, err := a.Stop(ctx, res.Ref)
	if err != nil || stopped.State != rt.StateStopped || stopped.HostPort != 0 {
		t.Fatalf("stop %+v %v", stopped, err)
	}
	if err = a.Remove(ctx, res.Ref); err != nil {
		t.Fatal(err)
	}
}

func TestV4NonRootImageGetsNoCapabilities(t *testing.T) {
	for _, user := range []string{"101", "nginx", "1000:1000"} {
		e := engineV4(user)
		res, err := newAdapter(e).Create(context.Background(), specV4(rt.AccessLocal))
		if err != nil || res.State != rt.StateRunning {
			t.Fatalf("%s: %+v %v", user, res, err)
		}
		if add := hostOf(e)["CapAdd"]; add != nil {
			t.Fatalf("%s: capabilities %v", user, add)
		}
		if e.container["Config"].(map[string]any)["User"] != user {
			t.Fatalf("%s: user not inherited", user)
		}
	}
	for _, user := range []string{"", "0", "root", "root:root", "0:0", ":5"} {
		e := engineV4(user)
		if _, err := newAdapter(e).Create(context.Background(), specV4(rt.AccessInternal)); err != nil {
			t.Fatalf("root %q: %v", user, err)
		}
		if len(hostOf(e)["CapAdd"].([]any)) != 3 {
			t.Fatalf("root %q lacks capabilities", user)
		}
	}
}

func TestV4InternalPublishesNothing(t *testing.T) {
	e := engineV4("")
	a := newAdapter(e)
	res, err := a.Create(context.Background(), specV4(rt.AccessInternal))
	if err != nil || res.HostPort != 0 || hostOf(e)["PortBindings"] != nil {
		t.Fatalf("%+v %v %v", res, err, hostOf(e)["PortBindings"])
	}
	hostOf(e)["PortBindings"] = map[string]any{"80/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}}
	if _, err = a.Stop(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) {
		t.Fatalf("internal with published port: %v", err)
	}
}

func TestV4SoloNetworkDisablesNeighbours(t *testing.T) {
	e := engineV4("")
	s := specV4(rt.AccessInternal)
	s.Project = ""
	if _, err := newAdapter(e).Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(e.networks) != 1 {
		t.Fatalf("networks %v", e.networks)
	}
	for name, n := range e.networks {
		if !strings.HasPrefix(name, "darsena-solo-") || n["Options"].(map[string]any)["com.docker.network.bridge.enable_icc"] != "false" {
			t.Fatalf("%s %v", name, n)
		}
	}
}

func TestV4RejectsUnsafeObservedContainers(t *testing.T) {
	set := func(key string, v any) func(*engine) { return func(e *engine) { hostOf(e)[key] = v } }
	binding := func(ip, port string) any {
		return map[string]any{"80/tcp": []any{map[string]any{"HostIp": ip, "HostPort": port}}}
	}
	caps := func(c ...any) func(*engine) { return set("CapAdd", c) }
	for _, tc := range []struct {
		name   string
		user   string
		mutate func(*engine)
	}{
		{"non-loopback host ip", "", set("PortBindings", binding("0.0.0.0", ""))},
		{"empty host ip", "", set("PortBindings", binding("", ""))},
		{"ipv6 loopback", "", set("PortBindings", binding("::1", ""))},
		{"fixed host port", "", set("PortBindings", binding("127.0.0.1", "8080"))},
		{"extra port", "", set("PortBindings", map[string]any{"80/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}, "443/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}})},
		{"two bindings on one port", "", set("PortBindings", map[string]any{"80/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}, map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}})},
		{"wrong port", "", set("PortBindings", map[string]any{"81/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}})},
		{"udp instead of tcp", "", set("PortBindings", map[string]any{"80/udp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": ""}}})},
		{"no binding", "", set("PortBindings", nil)},
		{"publish all", "", set("PublishAllPorts", true)},
		{"extra capability", "", caps("CAP_CHOWN", "CAP_SETGID", "CAP_SETUID", "CAP_NET_ADMIN")},
		{"replaced capability", "", caps("CAP_CHOWN", "CAP_SETGID", "CAP_SYS_ADMIN")},
		{"missing capability", "", caps("CAP_CHOWN", "CAP_SETGID")},
		{"duplicate capability", "", caps("CAP_CHOWN", "CAP_CHOWN", "CAP_SETGID")},
		{"mixed prefixes", "", caps("CAP_CHOWN", "SETGID", "CAP_SETUID")},
		{"no capabilities for root image", "", set("CapAdd", nil)},
		{"capabilities on non-root image", "101", caps("CAP_CHOWN", "CAP_SETGID", "CAP_SETUID")},
		{"capability on non-root image", "101", caps("CAP_NET_RAW")},
		{"capabilities not dropped", "", set("CapDrop", []any{})},
		{"only some capabilities dropped", "", set("CapDrop", []any{"NET_RAW"})},
		{"readonly root", "", set("ReadonlyRootfs", true)},
		{"memory", "", set("Memory", 1<<40)},
		{"swap", "", set("MemorySwap", -1)},
		{"cpu", "", set("NanoCpus", 0)},
		{"processes", "", set("PidsLimit", 4096)},
		{"no pid limit", "", set("PidsLimit", nil)},
		{"privileged", "", set("Privileged", true)},
		{"seccomp unconfined", "", set("SecurityOpt", []any{"seccomp=unconfined"})},
		{"new privileges", "", set("SecurityOpt", []any{})},
		{"host network", "", set("NetworkMode", "host")},
		{"default bridge", "", set("NetworkMode", "bridge")},
		{"other network mode", "", set("NetworkMode", "darsena-p-0000000000000000")},
		{"foreign network attached", "", func(e *engine) { netsOf(e)["bridge"] = map[string]any{} }},
		{"network missing", "", func(e *engine) {
			for k := range netsOf(e) {
				delete(netsOf(e), k)
				return
			}
		}},
		{"mounts", "", set("Mounts", []any{map[string]any{"Type": "bind"}})},
		{"binds", "", set("Binds", []any{"/:/host"})},
		{"tmpfs", "", set("Tmpfs", map[string]any{"/tmp": ""})},
		{"devices", "", set("Devices", []any{map[string]any{"PathOnHost": "/dev/kvm"}})},
		{"restart", "", set("RestartPolicy", map[string]any{"Name": "always"})},
		{"logs", "", set("LogConfig", map[string]any{"Type": "json-file"})},
		{"user changed", "101", func(e *engine) { e.container["Config"].(map[string]any)["User"] = "0" }},
		{"user changed for root image", "", func(e *engine) { e.container["Config"].(map[string]any)["User"] = "101" }},
		{"missing network settings", "", func(e *engine) { delete(e.container, "NetworkSettings") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engineV4(tc.user)
			a := newAdapter(e)
			s := specV4(rt.AccessLocal)
			s.Shared = true
			res, err := a.Create(context.Background(), s)
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(e)
			if _, err = a.Stop(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
			if e.container["State"].(map[string]any)["Running"] != true {
				t.Fatal("unsafe resource was mutated")
			}
			if err = a.Remove(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) || e.container == nil {
				t.Fatalf("remove %v", err)
			}
		})
	}
}

func TestV4RejectsUnsafeRuntimePorts(t *testing.T) {
	for name, ports := range map[string]any{
		"non-loopback": map[string]any{"80/tcp": []any{map[string]any{"HostIp": "0.0.0.0", "HostPort": "32768"}}},
		"other port":   map[string]any{"81/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "32768"}}},
		"two bindings": map[string]any{"80/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "32768"}, map[string]any{"HostIp": "127.0.0.1", "HostPort": "32769"}}},
		"bad number":   map[string]any{"80/tcp": []any{map[string]any{"HostIp": "127.0.0.1", "HostPort": "http"}}},
	} {
		e := engineV4("")
		a := newAdapter(e)
		res, err := a.Create(context.Background(), specV4(rt.AccessLocal))
		if err != nil {
			t.Fatal(err)
		}
		e.container["NetworkSettings"].(map[string]any)["Ports"] = ports
		if _, err = a.Inspect(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// Stopped services report no port; an unpublished running local service reports none either.
	e := engineV4("")
	a := newAdapter(e)
	res, _ := a.Create(context.Background(), specV4(rt.AccessLocal))
	e.container["NetworkSettings"].(map[string]any)["Ports"] = map[string]any{"80/tcp": nil}
	if got, err := a.Inspect(context.Background(), res.Ref); err != nil || got.HostPort != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestV4ReferenceAndV3ContainerDoNotCross(t *testing.T) {
	e := fake()
	a := newAdapter(e)
	v3, err := a.Create(context.Background(), spec())
	if err != nil {
		t.Fatal(err)
	}
	// Same identity, different policy: the labelled fingerprint differs.
	other, err := PrepareRef(specV4(rt.AccessLocal))
	if err != nil {
		t.Fatal(err)
	}
	if other.ResourceID != v3.Ref.ResourceID {
		t.Fatal("test needs one identity")
	}
	if _, err = a.Inspect(context.Background(), other); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("v4 ref on v3 container: %v", err)
	}
	if _, err = a.Create(context.Background(), specV4(rt.AccessLocal)); !errors.Is(err, rt.ErrConflict) {
		t.Fatalf("v4 create over v3 container: %v", err)
	}
}

func TestImageUserDecidesCapabilitiesNotTheContainerAlone(t *testing.T) {
	// A container claiming a non-root user that the image does not declare is rejected.
	e := engineV4("101")
	a := newAdapter(e)
	res, err := a.Create(context.Background(), specV4(rt.AccessLocal))
	if err != nil {
		t.Fatal(err)
	}
	e.imageUser = ""
	if _, err = a.Inspect(context.Background(), res.Ref); !errors.Is(err, rt.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
}

func preloadNetwork(e *engine, r rt.Ref, index int, mutate func(map[string]any)) {
	n := networkSpecs(r)[index]
	labels := map[string]any{}
	for k, v := range n.labels() {
		labels[k] = v
	}
	options := map[string]any{}
	for k, v := range n.options() {
		options[k] = v
	}
	stored := map[string]any{"Name": n.name, "Driver": "bridge", "Scope": "local", "Internal": false, "EnableIPv6": false, "Attachable": false, "Ingress": false, "ConfigOnly": false, "Labels": labels, "Options": options}
	mutate(stored)
	if e.networks == nil {
		e.networks = map[string]map[string]any{}
	}
	e.networks[n.name] = stored
}

func TestV4NetworkVerificationFailsClosed(t *testing.T) {
	ref, _ := PrepareRef(specV4(rt.AccessLocal))
	solo := specV4(rt.AccessInternal)
	solo.Project = ""
	soloRef, _ := PrepareRef(solo)
	label := func(k string, v any) func(map[string]any) {
		return func(n map[string]any) { n["Labels"].(map[string]any)[k] = v }
	}
	for _, tc := range []struct {
		name   string
		ref    rt.Ref
		spec   rt.Spec
		mutate func(map[string]any)
	}{
		{"no labels", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["Labels"] = nil }},
		{"empty labels", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["Labels"] = map[string]any{} }},
		{"foreign installation", ref, specV4(rt.AccessLocal), label(prefix+"installation", "someone-else")},
		{"foreign project", ref, specV4(rt.AccessLocal), label(prefix+"project", "proj-2")},
		{"wrong kind", ref, specV4(rt.AccessLocal), label(prefix+"network", "shared")},
		{"wrong schema", ref, specV4(rt.AccessLocal), label(prefix+"schema", "2")},
		{"other driver", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["Driver"] = "overlay" }},
		{"internal", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["Internal"] = true }},
		{"ipv6", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["EnableIPv6"] = true }},
		{"attachable", ref, specV4(rt.AccessLocal), func(n map[string]any) { n["Attachable"] = true }},
		{"unexpected option", ref, specV4(rt.AccessLocal), func(n map[string]any) {
			n["Options"].(map[string]any)["com.docker.network.bridge.enable_icc"] = "false"
		}},
		{"solo with icc enabled", soloRef, solo, func(n map[string]any) { delete(n["Options"].(map[string]any), "com.docker.network.bridge.enable_icc") }},
		{"solo with icc true", soloRef, solo, func(n map[string]any) { n["Options"].(map[string]any)["com.docker.network.bridge.enable_icc"] = "true" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := engineV4("")
			preloadNetwork(e, tc.ref, 0, tc.mutate)
			_, err := newAdapter(e).Create(context.Background(), tc.spec)
			if !errors.Is(err, rt.ErrConflict) || e.posts != 0 || e.netPosts != 0 || e.container != nil {
				t.Fatalf("err %v posts %d netPosts %d", err, e.posts, e.netPosts)
			}
		})
	}
	t.Run("shared network", func(t *testing.T) {
		e := engineV4("")
		s := specV4(rt.AccessLocal)
		s.Shared = true
		r, _ := PrepareRef(s)
		preloadNetwork(e, r, 0, func(map[string]any) {})
		preloadNetwork(e, r, 1, func(n map[string]any) { n["Labels"] = nil })
		if _, err := newAdapter(e).Create(context.Background(), s); !errors.Is(err, rt.ErrConflict) || e.posts != 0 {
			t.Fatalf("%v", err)
		}
	})
}

func TestV4ExistingVerifiedNetworkIsReused(t *testing.T) {
	e := engineV4("")
	ref, _ := PrepareRef(specV4(rt.AccessLocal))
	preloadNetwork(e, ref, 0, func(map[string]any) {})
	if _, err := newAdapter(e).Create(context.Background(), specV4(rt.AccessLocal)); err != nil || e.netPosts != 0 {
		t.Fatalf("%v %d", err, e.netPosts)
	}
}

func TestV4NetworkCreateRaceReinspects(t *testing.T) {
	e := engineV4("")
	e.netRace = true
	if res, err := newAdapter(e).Create(context.Background(), specV4(rt.AccessLocal)); err != nil || res.State != rt.StateRunning {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestV4AddressPoolExhaustionIsAFixedError(t *testing.T) {
	e := engineV4("")
	e.netExhausted = true
	_, err := newAdapter(e).Create(context.Background(), specV4(rt.AccessLocal))
	if !errors.Is(err, rt.ErrNetworkUnavailable) || e.posts != 0 || strings.Contains(err.Error(), "pool") {
		t.Fatalf("%v posts %d", err, e.posts)
	}
}

func TestV4CreateBodyBindsNetworksAndAliases(t *testing.T) {
	e := engineV4("")
	s := specV4(rt.AccessLocal)
	s.Shared = true
	res, err := newAdapter(e).Create(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	want := expectedNetworks(res.Ref)
	if len(want) != 2 || !strings.HasPrefix(want[0], "darsena-p-") || !strings.HasPrefix(want[1], "darsena-shared-") {
		t.Fatalf("names %v", want)
	}
	for _, n := range want {
		if _, ok := netsOf(e)[n]; !ok {
			t.Fatalf("missing %s", n)
		}
		if e.networks[n]["Labels"].(map[string]any)[prefix+"installation"] != "install-1" {
			t.Fatalf("unlabelled %s", n)
		}
	}
	if hostOf(e)["NetworkMode"] != want[0] {
		t.Fatalf("primary %v", hostOf(e)["NetworkMode"])
	}
	// Another project, same installation: a different primary network, same shared one.
	s2 := s
	s2.Project = "proj-2"
	r2, _ := PrepareRef(s2)
	n2 := expectedNetworks(r2)
	if n2[0] == want[0] || n2[1] != want[1] {
		t.Fatalf("%v vs %v", n2, want)
	}
}
