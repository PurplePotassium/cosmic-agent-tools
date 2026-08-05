package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/app"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/domain"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/store"
	"github.com/PurplePotassium/cosmic-agent-tools/hal/internal/workflow"
)

// Workflow REST surface. Every mutating action funnels through the workflow
// Manager (which serializes per-workflow via session inboxes); reads come
// straight from the store. Conflicting operator actions (message during a
// turn, double approval, stage mismatch) come back as 409s the dashboard
// renders inline.

func (s *Server) workflowRoutes(mux *http.ServeMux, guardRead, guard func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/v1/workflows", guardRead(s.getWorkflows))
	mux.HandleFunc("GET /api/v1/workflows/{id}", guardRead(s.getWorkflow))
	mux.HandleFunc("GET /api/v1/workflows/{id}/messages", guardRead(s.getWorkflowMessages))
	mux.HandleFunc("GET /api/v1/workflows/{id}/turns", guardRead(s.getWorkflowTurns))
	mux.HandleFunc("GET /api/v1/workflows/{id}/turns/{turn}/log", guardRead(s.getWorkflowTurnLog))
	mux.HandleFunc("GET /api/v1/workflows/{id}/artifacts/{stage}", guardRead(s.getWorkflowArtifact))
	mux.HandleFunc("GET /api/v1/workflows/{id}/diff", guardRead(s.getWorkflowDiff))
	mux.HandleFunc("GET /api/v1/validation", guardRead(s.getValidation))

	mux.HandleFunc("POST /api/v1/workflows", guard(s.postWorkflow))
	mux.HandleFunc("POST /api/v1/validation", guard(s.postValidation))
	mux.HandleFunc("PUT /api/v1/validation/auto", guard(s.putValidationAuto))
	mux.HandleFunc("POST /api/v1/workflows/{id}/messages", guard(s.postWorkflowMessage))
	mux.HandleFunc("POST /api/v1/workflows/{id}/interrupt", guard(s.postWorkflowInterrupt))
	mux.HandleFunc("POST /api/v1/workflows/{id}/approve", guard(s.postWorkflowApprove))
	mux.HandleFunc("POST /api/v1/workflows/{id}/reject", guard(s.postWorkflowReject))
	mux.HandleFunc("POST /api/v1/workflows/{id}/skip", guard(s.postWorkflowSkip))
	mux.HandleFunc("POST /api/v1/workflows/{id}/abandon", guard(s.postWorkflowAbandon))
	mux.HandleFunc("PUT /api/v1/workflows/{id}/artifacts/{stage}", guard(s.putWorkflowArtifact))
	mux.HandleFunc("PUT /api/v1/workflows/{id}/bundle", guard(s.putWorkflowBundle))
}

// wfErr maps manager/store errors onto HTTP statuses: unknown ids are 404,
// action-validation failures are 409 (the UI's "state moved on" signal).
func wfErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpErr(w, err, http.StatusNotFound)
	case errors.Is(err, app.ErrArtifactConflict):
		httpErr(w, err, http.StatusConflict)
	default:
		httpErr(w, err, http.StatusConflict)
	}
}

func (s *Server) getWorkflows(w http.ResponseWriter, r *http.Request) {
	list, err := s.App.Store.ListWorkflows(r.Context(), r.URL.Query().Get("all") != "")
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	detail, err := s.App.WorkflowDetail(r.Context(), r.PathValue("id"), true, 0, 0)
	if err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, detail)
}

func (s *Server) getWorkflowMessages(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 500
	}
	msgs, err := s.App.Store.ListMessages(r.Context(), r.PathValue("id"), after, limit)
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, msgs)
}

func (s *Server) getWorkflowTurns(w http.ResponseWriter, r *http.Request) {
	turns, err := s.App.Store.ListTurns(r.Context(), r.PathValue("id"))
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, turns)
}

func (s *Server) getWorkflowTurnLog(w http.ResponseWriter, r *http.Request) {
	turnID, err := strconv.ParseInt(r.PathValue("turn"), 10, 64)
	if err != nil {
		httpErr(w, fmt.Errorf("bad turn id"), http.StatusBadRequest)
		return
	}
	turn, err := s.App.Store.GetTurn(r.Context(), turnID)
	if err != nil {
		wfErr(w, err)
		return
	}
	// The id in the path scopes authorization to the workflow; a mismatched
	// pair is a client bug.
	if turn.WorkflowID != r.PathValue("id") || turn.LogPath == "" {
		httpErr(w, fmt.Errorf("no log for that turn"), http.StatusNotFound)
		return
	}
	data, err := os.ReadFile(turn.LogPath)
	if err != nil {
		httpErr(w, err, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(data)
}

func (s *Server) getWorkflowArtifact(w http.ResponseWriter, r *http.Request) {
	stage := domain.WorkflowStage(r.PathValue("stage"))
	if !domain.ValidWorkflowStage(string(stage)) {
		httpErr(w, fmt.Errorf("unknown stage %q", stage), http.StatusBadRequest)
		return
	}
	art, err := s.App.ReadWorkflowArtifact(r.PathValue("id"), stage)
	if err != nil {
		httpErr(w, err, http.StatusNotFound)
		return
	}
	writeJSON(w, art)
}

func (s *Server) getWorkflowDiff(w http.ResponseWriter, r *http.Request) {
	stat, err := s.App.WorkflowDiff(r.Context(), r.PathValue("id"))
	if err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]string{"stat": stat})
}

func (s *Server) postWorkflow(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title      string `json:"title"`
		Text       string `json:"text"`
		StartStage string `json:"startStage"`
		// Omitted means on: older API clients inherit auto-advance.
		AutoApprove *bool         `json:"autoApprove"`
		FromTaskID  string        `json:"fromTaskId"`
		Bundle      domain.Bundle `json:"bundle"`
		// StageBundles overrides model/effort per stage (beats bundle for
		// that stage), keyed by the fixed stage names.
		StageBundles map[string]domain.Bundle `json:"stageBundles"`
		Attachments  []string                 `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	stageBundles, err := parseStageBundles(body.StageBundles)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	autoApprove := body.AutoApprove == nil || *body.AutoApprove
	req := workflow.CreateReq{
		Title:        strings.TrimSpace(body.Title),
		Brief:        body.Text,
		StartStage:   domain.WorkflowStage(body.StartStage),
		AutoApprove:  autoApprove,
		Bundle:       body.Bundle,
		StageBundles: stageBundles,
		Attachments:  body.Attachments,
	}
	// Promoting an idea: the task's title/detail seed the workflow, and the
	// task row is closed with a pointer at it.
	if body.FromTaskID != "" {
		task, err := s.App.Store.GetTask(r.Context(), body.FromTaskID)
		if err != nil {
			wfErr(w, err)
			return
		}
		if req.Title == "" {
			req.Title = task.Title
		}
		if strings.TrimSpace(req.Brief) == "" {
			req.Brief = task.Title
			if task.Detail != "" {
				req.Brief = task.Title + "\n\n" + task.Detail
			}
		}
	}
	wf, err := s.App.WorkflowManager().Create(r.Context(), req)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if body.FromTaskID != "" {
		_ = s.App.Store.CompleteTask(r.Context(), body.FromTaskID, "", "promoted to workflow "+wf.ID)
	}
	writeJSON(w, wf)
}

// getValidation returns the validation card's view model: the auto toggle,
// pending implementations, and the live run (if any).
func (s *Server) getValidation(w http.ResponseWriter, r *http.Request) {
	st, err := s.App.ValidationState(r.Context())
	if err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, st)
}

// postValidation triggers a validation run (the Validate button). 409 when
// one is already live.
func (s *Server) postValidation(w http.ResponseWriter, r *http.Request) {
	run, err := s.App.WorkflowManager().StartValidation(r.Context())
	if err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, run)
}

// putValidationAuto persists the auto-validate-on-completion toggle.
func (s *Server) putValidationAuto(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.WorkflowManager().SetAutoValidate(r.Context(), body.On); err != nil {
		httpErr(w, err, http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]bool{"on": body.On})
}

func (s *Server) postWorkflowMessage(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Text      string `json:"text"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.WorkflowManager().Message(r.Context(), r.PathValue("id"), body.Text, body.Interrupt); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) postWorkflowInterrupt(w http.ResponseWriter, r *http.Request) {
	if err := s.App.WorkflowManager().Interrupt(r.Context(), r.PathValue("id")); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) postWorkflowApprove(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stage string `json:"stage"`
		Note  string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.WorkflowManager().Approve(r.Context(), r.PathValue("id"), domain.WorkflowStage(body.Stage), body.Note); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) postWorkflowReject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stage    string `json:"stage"`
		Feedback string `json:"feedback"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.WorkflowManager().Reject(r.Context(), r.PathValue("id"), domain.WorkflowStage(body.Stage), body.Feedback); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) postWorkflowSkip(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Stage string `json:"stage"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	if err := s.App.WorkflowManager().Skip(r.Context(), r.PathValue("id"), domain.WorkflowStage(body.Stage)); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) postWorkflowAbandon(w http.ResponseWriter, r *http.Request) {
	if err := s.App.WorkflowManager().Abandon(r.Context(), r.PathValue("id")); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) putWorkflowArtifact(w http.ResponseWriter, r *http.Request) {
	stage := domain.WorkflowStage(r.PathValue("stage"))
	if !domain.ValidWorkflowStage(string(stage)) {
		httpErr(w, fmt.Errorf("unknown stage %q", stage), http.StatusBadRequest)
		return
	}
	var body struct {
		Content  string `json:"content"`
		BaseHash string `json:"baseHash"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	art, err := s.App.WriteWorkflowArtifact(r.Context(), r.PathValue("id"), stage, body.Content, body.BaseHash)
	if err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, art)
}

// parseStageBundles validates a per-stage override map at the API boundary:
// stage names must be the fixed ladder's, efforts must be recognized. Zero
// entries are dropped; an empty result is nil (no overrides).
func parseStageBundles(in map[string]domain.Bundle) (map[domain.WorkflowStage]domain.Bundle, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := map[domain.WorkflowStage]domain.Bundle{}
	for stage, b := range in {
		if !domain.ValidWorkflowStage(stage) {
			return nil, fmt.Errorf("stageBundles.%s: unknown stage (stages: refine, research, design, plan, implement, validate)", stage)
		}
		if !domain.ValidEffort(b.Effort) {
			return nil, fmt.Errorf("stageBundles.%s: effort %q is not one of %v", stage, b.Effort, domain.Efforts)
		}
		if !b.IsZero() {
			out[domain.WorkflowStage(stage)] = b
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s *Server) putWorkflowBundle(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Agent  string `json:"agent"`
		Model  string `json:"model"`
		Effort string `json:"effort"`
		// Stages: per-stage model/effort overrides, beating the base fields
		// for that stage. Absent/empty = base only.
		Stages map[string]domain.Bundle `json:"stages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	stages, err := parseStageBundles(body.Stages)
	if err != nil {
		httpErr(w, err, http.StatusBadRequest)
		return
	}
	base := domain.Bundle{Agent: body.Agent, Model: body.Model, Effort: body.Effort}
	if err := s.App.Store.SetWorkflowBundle(r.Context(), r.PathValue("id"), base, stages); err != nil {
		wfErr(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}
