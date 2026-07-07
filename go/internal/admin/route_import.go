package admin

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nyroway/nyro/go/internal/storage"
)

type routeImportEvent struct {
	Type       string `json:"type"`
	Stage      string `json:"stage,omitempty"`
	Status     string `json:"status,omitempty"`
	Message    string `json:"message,omitempty"`
	Model      string `json:"model,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Error      string `json:"error,omitempty"`
	Count      int    `json:"count,omitempty"`
	Success    bool   `json:"success,omitempty"`
	Discovered int    `json:"discovered,omitempty"`
	Created    int    `json:"created,omitempty"`
	Skipped    int    `json:"skipped,omitempty"`
	Failed     int    `json:"failed,omitempty"`
}

type routeImportPreview struct {
	Discovered int      `json:"discovered"`
	Create     []string `json:"create"`
	Skip       []string `json:"skip"`
}

type routeImportEventWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

func newRouteImportEventWriter(w http.ResponseWriter) *routeImportEventWriter {
	flusher, _ := w.(http.Flusher)
	return &routeImportEventWriter{w: w, flusher: flusher}
}

func (e *routeImportEventWriter) send(ev routeImportEvent) {
	b, _ := json.Marshal(ev)
	_, _ = e.w.Write([]byte("event: route_import\n"))
	_, _ = e.w.Write([]byte("data: "))
	_, _ = e.w.Write(b)
	_, _ = e.w.Write([]byte("\n\n"))
	if e.flusher != nil {
		e.flusher.Flush()
	}
}

func previewUpstreamRouteImport(w http.ResponseWriter, r *http.Request, s storage.Storage, u storage.Upstream) {
	plan, err := planUpstreamRouteImport(r, s, u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(plan)
}

func streamImportUpstreamRoutes(w http.ResponseWriter, r *http.Request, s storage.Storage, u storage.Upstream) {
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	events := newRouteImportEventWriter(w)

	events.send(routeImportEvent{Type: "stage", Stage: "models", Status: "running", Message: "Resolving models"})
	plan, err := planUpstreamRouteImport(r, s, u)
	if err != nil {
		events.send(routeImportEvent{Type: "stage", Stage: "models", Status: "failed", Error: err.Error()})
		events.send(routeImportEvent{Type: "complete", Success: false, Error: err.Error()})
		return
	}
	events.send(routeImportEvent{Type: "stage", Stage: "models", Status: "passed", Count: plan.Discovered, Message: "Models resolved"})

	events.send(routeImportEvent{Type: "stage", Stage: "creating", Status: "running", Message: "Creating missing routes"})
	created, skipped, failed := 0, len(plan.Skip), 0
	for _, model := range plan.Skip {
		events.send(routeImportEvent{Type: "route", Model: model, Status: "skipped", Reason: "route already exists"})
	}
	for _, model := range plan.Create {
		exists, err := s.Routes().ExistsByName(model, "")
		if err != nil {
			failed++
			events.send(routeImportEvent{Type: "route", Model: model, Status: "failed", Error: err.Error()})
			continue
		}
		if exists {
			skipped++
			events.send(routeImportEvent{Type: "route", Model: model, Status: "skipped", Reason: "route already exists"})
			continue
		}
		_, err = s.Routes().Create(storage.CreateRoute{
			Model:      model,
			Balance:    storage.BalanceWeighted,
			EnableAuth: false,
			Upstreams: []storage.CreateRouteUpstream{{
				UpstreamID: u.ID,
				Model:      model,
				Weight:     100,
				Priority:   1,
			}},
		})
		if err != nil {
			failed++
			events.send(routeImportEvent{Type: "route", Model: model, Status: "failed", Error: err.Error()})
			continue
		}
		created++
		events.send(routeImportEvent{Type: "route", Model: model, Status: "created"})
	}
	if created > 0 {
		bumpEpoch(s)
	}
	success := failed == 0
	status := "passed"
	if !success {
		status = "failed"
	}
	events.send(routeImportEvent{Type: "stage", Stage: "creating", Status: status, Count: created, Message: "Route import finished"})
	events.send(routeImportEvent{
		Type:       "complete",
		Success:    success,
		Discovered: plan.Discovered,
		Created:    created,
		Skipped:    skipped,
		Failed:     failed,
	})
}

func planUpstreamRouteImport(r *http.Request, s storage.Storage, u storage.Upstream) (routeImportPreview, error) {
	models, err := modelsForUpstream(r.Context(), u)
	if err != nil {
		return routeImportPreview{}, err
	}
	models = normalizeImportModels(models)
	plan := routeImportPreview{Discovered: len(models)}
	for _, model := range models {
		exists, err := s.Routes().ExistsByName(model, "")
		if err != nil {
			return routeImportPreview{}, err
		}
		if exists {
			plan.Skip = append(plan.Skip, model)
		} else {
			plan.Create = append(plan.Create, model)
		}
	}
	return plan, nil
}

func normalizeImportModels(models []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		trimmed := strings.TrimSpace(model)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		out = append(out, trimmed)
	}
	return out
}
