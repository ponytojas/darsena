package worker

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/ponytojas/darsena/internal/platform/jobs"
	"github.com/ponytojas/darsena/internal/platform/runtime/intent"
	"github.com/ponytojas/darsena/internal/platform/runtime/reconcile"
)

// configSummary is the current encrypted configuration revision, never values.
// UpdatedAt is null for revisions written before dates were recorded.
type configSummary struct {
	Revision  int64      `json:"revision"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// serviceSummary is the persisted state of the experimental service. It has no
// dates: the intention store records none.
type serviceSummary struct {
	Phase       intent.Phase          `json:"phase"`
	Observation reconcile.Observation `json:"observation"`
	Version     uint64                `json:"version"`
	Action      *intent.Action        `json:"action,omitempty"`
}

// applicationSummary is what GET /applications and GET /applications/{id}
// return. Service is null without the experimental runtime, without a service,
// or when it could not be read; ServiceUnavailable distinguishes the last case.
// ConfigUnavailable does the same for Config.
type applicationSummary struct {
	jobs.Application
	Config             *configSummary  `json:"config"`
	ConfigUnavailable  bool            `json:"config_unavailable"`
	Service            *serviceSummary `json:"service"`
	ServiceUnavailable bool            `json:"service_unavailable"`
}

// summaries enriches applications using a fixed number of queries on each
// store (applications are read by the caller): one for the configuration
// revisions and two for the services.
func (s *Service) summaries(ctx context.Context, apps []jobs.Application) []applicationSummary {
	out := make([]applicationSummary, len(apps))
	for i, app := range apps {
		out[i].Application = app
	}
	if s.vault != nil {
		revisions, err := s.vault.Revisions(ctx)
		for i, app := range apps {
			if err != nil {
				out[i].ConfigUnavailable = true
			} else if info, ok := revisions[app.ID]; ok {
				out[i].Config = &configSummary{Revision: info.Revision, UpdatedAt: info.UpdatedAt}
			}
		}
	}
	if s.runtime != nil {
		ids := make([]string, len(apps))
		for i, app := range apps {
			ids[i] = app.ID
		}
		services, err := s.runtime.store.ListServices(ctx, s.runtime.installation, serviceOperation, ids)
		for i, app := range apps {
			if err != nil {
				out[i].ServiceUnavailable = true
				continue
			}
			if sum, ok := services[app.ID]; ok {
				if sum.Invalid {
					out[i].ServiceUnavailable = true
				} else {
					out[i].Service = &serviceSummary{Phase: sum.Intent.Phase, Observation: sum.Intent.Observation, Version: sum.Intent.Version, Action: sum.Action}
				}
			}
		}
	}
	return out
}

func (s *Service) listApplications(w http.ResponseWriter, r *http.Request) {
	apps, err := s.store.Applications(r.Context())
	if err != nil {
		failure(w, 500, "unable to list applications")
		return
	}
	send(w, 200, s.summaries(r.Context(), apps))
}
func (s *Service) getApplication(w http.ResponseWriter, r *http.Request) {
	app, err := s.store.Application(r.Context(), r.PathValue("id"))
	if errors.Is(err, jobs.ErrNotFound) {
		failureCode(w, 404, "application_not_found", "application not found")
		return
	}
	if err != nil {
		failure(w, 500, "unable to read application")
		return
	}
	send(w, 200, s.summaries(r.Context(), []jobs.Application{app})[0])
}
