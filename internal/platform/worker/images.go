package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	rt "github.com/ponytojas/darsena/internal/platform/runtime"
)

// ImageProvider is the image part of the experimental runtime: availability,
// public tag resolution and explicit pulls, all without registry credentials.
type ImageProvider interface {
	ImageStatus(context.Context, string) (rt.ImageStatus, error)
	ResolveTag(context.Context, string) (string, error)
	RegistrySupports(context.Context, string) (bool, string, error)
	Pull(context.Context, string) error
}

const pullTimeout = 10 * time.Minute

// imagePulls tracks explicit pulls in memory: one runs at a time, and a failure
// is remembered until the next attempt or a restart. The daemon remains the
// source of truth for availability.
type imagePulls struct {
	mu     sync.Mutex
	active string
	failed map[string]string // pinned reference -> reason
	ctx    context.Context
	cancel context.CancelFunc
}

func newImagePulls() *imagePulls {
	ctx, cancel := context.WithCancel(context.Background())
	return &imagePulls{failed: map[string]string{}, ctx: ctx, cancel: cancel}
}

// pullState returns "pulling", "pull_failed" with a reason, or "".
func (p *imagePulls) pullState(ref string) (string, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == ref {
		return "pulling", ""
	}
	if reason, ok := p.failed[ref]; ok {
		return "pull_failed", reason
	}
	return "", ""
}

type imageResponse struct {
	Reference      string `json:"reference"`
	ResolvedFrom   string `json:"resolved_from,omitempty"`
	Status         string `json:"status"`
	Reason         string `json:"reason,omitempty"`
	Platform       string `json:"platform,omitempty"`
	DaemonPlatform string `json:"daemon_platform,omitempty"`
}

// checkImage normalizes a reference typed by a person. Without the runtime it
// only normalizes and requires a digest; with it, a public tag is resolved to
// its current digest and the daemon's copy is inspected.
func (s *Service) checkImage(w http.ResponseWriter, r *http.Request) {
	ref, err := rt.ParseImage(r.URL.Query().Get("ref"))
	if err != nil {
		failureCode(w, 400, "invalid_image", "invalid image reference")
		return
	}
	out := imageResponse{Status: "not_checked"}
	if ref.Digest == "" {
		if s.runtime == nil || s.runtime.images == nil {
			failureCode(w, 400, "image_digest_required", "image must be pinned by sha256 digest")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		digest, err := s.runtime.images.ResolveTag(ctx, ref.Tagged())
		if errors.Is(err, rt.ErrNotFound) {
			failureCode(w, 404, "image_not_found", "image or tag not found in a public registry")
			return
		}
		if err != nil {
			failureCode(w, 503, "registry_unavailable", "registry could not be reached")
			return
		}
		out.ResolvedFrom = ref.Tagged()
		ref.Digest = digest
	}
	out.Reference = ref.Pinned()
	if s.runtime == nil || s.runtime.images == nil {
		send(w, 200, out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	status, err := s.runtime.images.ImageStatus(ctx, out.Reference)
	if err != nil {
		out.Status = "unknown"
		send(w, 200, out)
		return
	}
	out.Status, out.Reason, out.Platform, out.DaemonPlatform = string(status.State), status.Reason, status.Platform, status.DaemonPlatform
	if status.State == rt.ImageMissing {
		if state, reason := s.runtime.pulls.pullState(out.Reference); state != "" {
			out.Status, out.Reason = state, reason
		} else if ok, platform, err := s.runtime.images.RegistrySupports(ctx, out.Reference); err == nil && !ok {
			// Say so before a download that could only fail.
			out.Status, out.Reason, out.Platform = string(rt.ImageIncompatible), "platform", platform
		}
	}
	send(w, 200, out)
}

// pullImage starts an explicit background pull of a pinned public image.
func (s *Service) pullImage(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reference string `json:"reference"`
	}
	if !configBody(w, r, &in) {
		return
	}
	ref, err := rt.ParseImage(in.Reference)
	if err != nil || ref.Digest == "" {
		failureCode(w, 400, "invalid_image", "image must be pinned by sha256 digest")
		return
	}
	pinned := ref.Pinned()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if status, err := s.runtime.images.ImageStatus(ctx, pinned); err == nil && status.State != rt.ImageMissing {
		send(w, 200, imageResponse{Reference: pinned, Status: string(status.State), Reason: status.Reason, Platform: status.Platform, DaemonPlatform: status.DaemonPlatform})
		return
	}
	p := s.runtime.pulls
	p.mu.Lock()
	switch p.active {
	case pinned:
		p.mu.Unlock()
		send(w, 202, imageResponse{Reference: pinned, Status: "pulling"})
		return
	case "":
	default:
		p.mu.Unlock()
		failureCode(w, 409, "pull_busy", "another image is being downloaded")
		return
	}
	p.active = pinned
	delete(p.failed, pinned)
	p.mu.Unlock()
	slog.Info("image_pull_started", "component", "worker")
	go func() {
		ctx, cancel := context.WithTimeout(p.ctx, pullTimeout)
		defer cancel()
		err := s.runtime.images.Pull(ctx, pinned)
		p.mu.Lock()
		p.active = ""
		if err != nil {
			p.failed[pinned] = pullReason(err)
		}
		p.mu.Unlock()
		if err != nil {
			slog.Warn("image_pull_failed", "component", "worker", "reason", pullReason(err))
			return
		}
		slog.Info("image_pull_completed", "component", "worker")
	}()
	send(w, 202, imageResponse{Reference: pinned, Status: "pulling"})
}

func pullReason(err error) string {
	switch {
	case errors.Is(err, rt.ErrImagePlatform):
		return "platform"
	case errors.Is(err, rt.ErrNotFound):
		return "not_found"
	case errors.Is(err, rt.ErrTimeout):
		return "timeout"
	default:
		return "unavailable"
	}
}
