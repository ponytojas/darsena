package runtime

import (
	"strings"
	"testing"
)

func TestParseImage(t *testing.T) {
	d := "sha256:" + strings.Repeat("a", 64)
	cases := []struct{ in, name, tag, pinned string }{
		{"hello-world:nanoserver-ltsc2025@" + d, "docker.io/library/hello-world", "nanoserver-ltsc2025", "docker.io/library/hello-world@" + d},
		{"nginx", "docker.io/library/nginx", "", ""},
		{"  nginx:1.27  ", "docker.io/library/nginx", "1.27", ""},
		{"bitnami/redis@" + d, "docker.io/bitnami/redis", "", "docker.io/bitnami/redis@" + d},
		{"index.docker.io/library/nginx@" + d, "docker.io/library/nginx", "", "docker.io/library/nginx@" + d},
		{"GHCR.io/org/app:v1@sha256:" + strings.Repeat("A", 64), "ghcr.io/org/app", "v1", "ghcr.io/org/app@" + d},
		{"localhost:5000/team/app@" + d, "localhost:5000/team/app", "", "localhost:5000/team/app@" + d},
		{"registry.k8s.io/pause:3.10", "registry.k8s.io/pause", "3.10", ""},
		{"localhost/app:dev", "localhost/app", "dev", ""},
	}
	for _, c := range cases {
		ref, err := ParseImage(c.in)
		if err != nil || ref.Name != c.name || ref.Tag != c.tag || ref.Pinned() != c.pinned {
			t.Errorf("%q: got %+v pinned=%q err=%v", c.in, ref, ref.Pinned(), err)
		}
		if c.pinned != "" && (Spec{Identity: Identity{"i", "s", "o"}, Image: ref.Pinned(), Revision: 1}).Validate() != nil {
			t.Errorf("%q: pinned form rejected by Spec", c.in)
		}
	}
	for _, bad := range []string{"", "Nginx", "nginx@sha256:short", "user:pass@host/app", "https://ghcr.io/app", "ngi nx", "nginx:bad/tag", "a//b", strings.Repeat("a", 513), "nginx:" + strings.Repeat("t", 129), "-bad/app"} {
		if _, err := ParseImage(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if (ImageRef{Name: "docker.io/library/nginx"}).Tagged() != "docker.io/library/nginx:latest" {
		t.Error("default tag")
	}
}
