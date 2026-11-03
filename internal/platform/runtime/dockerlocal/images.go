package dockerlocal

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

// maxPullLine bounds one progress line of an image pull stream.
const maxPullLine = 64 << 10

var digestValue = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// platform returns the daemon's "os/arch".
func (a *Adapter) platform(ctx context.Context) (string, error) {
	status, b, err := a.request(ctx, http.MethodGet, "/version", nil, false)
	if err != nil {
		return "", err
	}
	var v struct{ Os, Arch string }
	if status != 200 || decode(b, &v) != nil || v.Os == "" || v.Arch == "" {
		return "", rt.ErrUnavailable
	}
	return v.Os + "/" + v.Arch, nil
}

// ImageStatus reports whether a pinned image is present in the daemon and
// usable by the isolated policy. It never pulls.
func (a *Adapter) ImageStatus(ctx context.Context, pinned string) (rt.ImageStatus, error) {
	ref, err := rt.ParseImage(pinned)
	if err != nil || ref.Pinned() != pinned {
		return rt.ImageStatus{}, rt.ErrInvalid
	}
	daemon, err := a.platform(ctx)
	if err != nil {
		return rt.ImageStatus{}, err
	}
	out := rt.ImageStatus{DaemonPlatform: daemon}
	status, b, err := a.request(ctx, http.MethodGet, api+"/images/"+url.PathEscape(pinned)+"/json", nil, false)
	if err != nil {
		return rt.ImageStatus{}, err
	}
	if status == 404 {
		out.State = rt.ImageMissing
		return out, nil
	}
	var img struct {
		ID           string `json:"Id"`
		Os           string
		Architecture string
		Config       struct{ Volumes map[string]json.RawMessage }
	}
	if status != 200 || decode(b, &img) != nil || !strings.HasPrefix(img.ID, "sha256:") || img.Os == "" || img.Architecture == "" {
		return rt.ImageStatus{}, rt.ErrUnavailable
	}
	out.Platform = img.Os + "/" + img.Architecture
	switch {
	case out.Platform != daemon:
		out.State, out.Reason = rt.ImageIncompatible, "platform"
	case len(img.Config.Volumes) != 0:
		out.State, out.Reason = rt.ImageIncompatible, "volumes"
	default:
		out.State = rt.ImageAvailable
	}
	return out, nil
}

// ResolveTag asks the registry, through the daemon and without credentials,
// for the current digest of name:tag. Only public images resolve.
func (a *Adapter) ResolveTag(ctx context.Context, tagged string) (string, error) {
	ref, err := rt.ParseImage(tagged)
	if err != nil || ref.Digest != "" || ref.Tag == "" || ref.Tagged() != tagged {
		return "", rt.ErrInvalid
	}
	digest, _, err := a.distribution(ctx, tagged)
	return digest, err
}

// RegistrySupports reports whether the registry offers a variant of a pinned
// public image for the daemon's platform. It returns rt.ErrNotFound for an
// unknown or private image; a registry without platform data counts as
// supported, since only a pull can tell.
func (a *Adapter) RegistrySupports(ctx context.Context, pinned string) (bool, string, error) {
	ref, err := rt.ParseImage(pinned)
	if err != nil || ref.Pinned() != pinned {
		return false, "", rt.ErrInvalid
	}
	daemon, err := a.platform(ctx)
	if err != nil {
		return false, "", err
	}
	_, platforms, err := a.distribution(ctx, pinned)
	if err != nil {
		return false, "", err
	}
	if len(platforms) == 0 {
		return true, "", nil
	}
	for _, p := range platforms {
		if p == daemon {
			return true, p, nil
		}
	}
	return false, platforms[0], nil
}

// distribution reads a reference's digest and os/arch platforms from its registry.
func (a *Adapter) distribution(ctx context.Context, reference string) (string, []string, error) {
	status, b, err := a.request(ctx, http.MethodGet, api+"/distribution/"+url.PathEscape(reference)+"/json", nil, false)
	if err != nil {
		return "", nil, err
	}
	switch status {
	case 200:
	case 401, 403, 404:
		return "", nil, rt.ErrNotFound
	default:
		return "", nil, rt.ErrUnavailable
	}
	var d struct {
		Descriptor struct {
			Digest string `json:"digest"`
		}
		Platforms []struct {
			OS           string `json:"os"`
			Architecture string `json:"architecture"`
		}
	}
	if decode(b, &d) != nil || !digestValue.MatchString(d.Descriptor.Digest) {
		return "", nil, rt.ErrUnavailable
	}
	var platforms []string
	for _, p := range d.Platforms {
		// Attestation manifests are listed as unknown/unknown.
		if p.OS != "" && p.OS != "unknown" && p.Architecture != "" && p.Architecture != "unknown" {
			platforms = append(platforms, p.OS+"/"+p.Architecture)
		}
	}
	return d.Descriptor.Digest, platforms, nil
}

// Pull downloads a pinned public image into the daemon without registry
// credentials. Progress output is read and discarded; daemon messages are
// mapped to fixed categories and never returned. The caller bounds the time
// with ctx. Success is confirmed by inspecting the image afterwards.
func (a *Adapter) Pull(ctx context.Context, pinned string) error {
	ref, err := rt.ParseImage(pinned)
	if err != nil || ref.Pinned() != pinned {
		return rt.ErrInvalid
	}
	if ctx.Err() != nil {
		return contextError(ctx)
	}
	query := url.Values{"fromImage": {ref.Name}, "tag": {ref.Digest}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://docker"+api+"/images/create?"+query.Encode(), nil)
	if err != nil {
		return rt.ErrInvalid
	}
	resp, err := a.long.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return contextError(ctx)
		}
		return rt.ErrUnavailable
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case 200:
	case 401, 403, 404:
		return rt.ErrNotFound
	default:
		return rt.ErrUnavailable
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 4096), maxPullLine)
	for scanner.Scan() {
		var line struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(scanner.Bytes(), &line) != nil || line.Error == "" {
			continue
		}
		msg := strings.ToLower(line.Error)
		switch {
		case strings.Contains(msg, "no matching manifest"):
			return rt.ErrImagePlatform
		case strings.Contains(msg, "not found"), strings.Contains(msg, "manifest unknown"), strings.Contains(msg, "denied"), strings.Contains(msg, "unauthorized"):
			return rt.ErrNotFound
		default:
			return rt.ErrUnavailable
		}
	}
	if scanner.Err() != nil {
		if ctx.Err() != nil {
			return contextError(ctx)
		}
		return rt.ErrUnavailable
	}
	status, err := a.ImageStatus(ctx, pinned)
	if err != nil {
		return err
	}
	if status.State == rt.ImageMissing {
		return rt.ErrUnavailable
	}
	return nil
}
