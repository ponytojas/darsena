package runtime

import (
	"errors"
	"regexp"
	"strings"
)

// ImageRef is a parsed container image reference in canonical form: Name always
// includes the registry and, for Docker Hub official images, "library/".
type ImageRef struct {
	Name   string // e.g. docker.io/library/nginx
	Tag    string // empty when absent; dropped from Pinned
	Digest string // "sha256:<64 lowercase hex>" or empty
}

var (
	pathComponent = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*$`)
	registryHost  = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)(?:\.[a-zA-Z0-9](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*(?::[0-9]+)?$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
	digestPattern = regexp.MustCompile(`^sha256:[a-fA-F0-9]{64}$`)
)

// ParseImage accepts the reference forms people usually type ("nginx",
// "nginx:1.27", "hello-world:tag@sha256:…", "ghcr.io/org/app@sha256:…") and
// returns their canonical parts. It rejects credentials, schemes, whitespace,
// uppercase repository names and anything longer than 512 bytes. It never
// contacts a registry.
func ParseImage(input string) (ImageRef, error) {
	s := strings.TrimSpace(input)
	if s == "" || len(s) > 512 || strings.ContainsAny(s, " \t\r\n") || strings.Contains(s, "://") {
		return ImageRef{}, ErrInvalid
	}
	var ref ImageRef
	if name, digest, ok := strings.Cut(s, "@"); ok {
		if !digestPattern.MatchString(digest) {
			return ImageRef{}, ErrInvalid
		}
		ref.Digest = strings.ToLower(digest)
		s = name
	}
	// A tag follows the last colon after the last slash; earlier colons
	// belong to a registry port.
	if i := strings.LastIndex(s, ":"); i > strings.LastIndex(s, "/") {
		ref.Tag = s[i+1:]
		s = s[:i]
		if !tagPattern.MatchString(ref.Tag) {
			return ImageRef{}, ErrInvalid
		}
	}
	parts := strings.Split(s, "/")
	domain := "docker.io"
	if len(parts) > 1 && (strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		domain, parts = parts[0], parts[1:]
		if !registryHost.MatchString(domain) {
			return ImageRef{}, ErrInvalid
		}
		domain = strings.ToLower(domain)
	}
	if domain == "index.docker.io" {
		domain = "docker.io"
	}
	if domain == "docker.io" && len(parts) == 1 {
		parts = append([]string{"library"}, parts...)
	}
	for _, p := range parts {
		if !pathComponent.MatchString(p) {
			return ImageRef{}, ErrInvalid
		}
	}
	ref.Name = domain + "/" + strings.Join(parts, "/")
	if ref.Digest != "" && !imagePattern.MatchString(ref.Pinned()) {
		return ImageRef{}, ErrInvalid
	}
	return ref, nil
}

// Pinned returns the canonical name@digest form, or "" without a digest.
func (r ImageRef) Pinned() string {
	if r.Digest == "" {
		return ""
	}
	return r.Name + "@" + r.Digest
}

// Tagged returns name:tag, defaulting to "latest", for registry resolution.
func (r ImageRef) Tagged() string {
	if r.Tag == "" {
		return r.Name + ":latest"
	}
	return r.Name + ":" + r.Tag
}

// ImageState is the availability of an image in the provider.
type ImageState string

const (
	ImageAvailable    ImageState = "available"
	ImageMissing      ImageState = "missing"
	ImageIncompatible ImageState = "incompatible"
)

// ImageStatus describes a pinned image in the provider. Reason is "platform"
// or "volumes" for an incompatible image. Platforms are "os/arch" strings.
type ImageStatus struct {
	State          ImageState
	Reason         string
	Platform       string
	DaemonPlatform string
}

// ErrImagePlatform reports that a registry has no variant of an image for the
// provider's platform.
var ErrImagePlatform = errors.New("runtime: image has no variant for this platform")
