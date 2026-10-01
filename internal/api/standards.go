package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
	store "github.com/goalforge/goalforge/internal/store/sqlite"
)

// StandardsView is one project's standing against the pack it pinned.
//
// The unchecked count is carried separately from the criteria themselves
// because a screen that only lists what was measured reads as coverage of a
// catalogue nobody finished walking — the same reason the CLI says it.
type StandardsView struct {
	PackRef string `json:"pack_ref"`
	// Enrolled is false when the project never pinned a pack. The screen says
	// so rather than showing an empty table, which reads as "nothing wrong".
	Enrolled bool `json:"enrolled"`
	// Drifted is true when the pinned pack's content changed under the
	// project.
	Drifted  bool                   `json:"drifted"`
	Criteria []store.AssessmentView `json:"criteria"`
	Counts   map[string]int         `json:"counts"`
	// Exceptions are the criteria somebody decided not to apply, with who
	// decided. An excused criterion and a met one look the same in a count and
	// mean completely different things.
	Exceptions []standards.Exception `json:"exceptions"`
	// OutOfProfile lists criteria that do not apply to this project, so a
	// reader can tell "not our shape" from "not done".
	OutOfProfile []string `json:"out_of_profile"`
}

func (s *Server) handleStandards(w http.ResponseWriter, r *http.Request, projectID string) {
	profile, _, err := s.store.StandardProfile(r.Context(), projectID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, StandardsView{Enrolled: false,
			Criteria: []store.AssessmentView{}, Counts: map[string]int{}})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pack, err := standards.ByRef(profile.PackRef)
	if err != nil {
		// Pinned to a catalogue this build does not carry. Saying so is the
		// difference between an empty screen and a screen that explains
		// itself.
		writeJSON(w, http.StatusOK, StandardsView{Enrolled: true, PackRef: profile.PackRef,
			Criteria: []store.AssessmentView{}, Counts: map[string]int{}, Drifted: true})
		return
	}
	now := time.Now()
	views, err := s.store.AssessmentsFor(r.Context(), projectID, pack, profile, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	drifted, err := s.store.PackDrift(r.Context(), projectID, pack)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	result := StandardsView{PackRef: pack.Ref(), Enrolled: true, Drifted: drifted,
		Criteria: views, Counts: map[string]int{}, Exceptions: profile.Exceptions,
		OutOfProfile: []string{}}
	if result.Criteria == nil {
		result.Criteria = []store.AssessmentView{}
	}
	for _, view := range views {
		result.Counts[view.Result]++
	}
	for _, applicable := range standards.Apply(pack, profile, now) {
		if applicable.OutOfProfile {
			result.OutOfProfile = append(result.OutOfProfile, applicable.Standard.ID)
		}
	}
	if result.Exceptions == nil {
		result.Exceptions = []standards.Exception{}
	}
	writeJSON(w, http.StatusOK, result)
}

// FleetView is one pack across every project pinned to it.
type FleetView struct {
	store.FleetReport
	Proposals []store.PackProposal `json:"proposals"`
}

// handleFleet reports one pack across its projects. The pack is a query
// parameter because a fleet is per catalogue: projects on different packs are
// not comparable, and folding them together would average two different
// questions.
func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request) {
	pack, err := standards.ByRef(r.URL.Query().Get("pack"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	now := time.Now()
	report, err := s.store.Fleet(r.Context(), pack, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	proposals, err := s.store.ProposePackChanges(r.Context(), pack, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if proposals == nil {
		proposals = []store.PackProposal{}
	}
	if report.Criteria == nil {
		report.Criteria = []store.CriterionAcross{}
	}
	if report.Projects == nil {
		report.Projects = []string{}
	}
	writeJSON(w, http.StatusOK, FleetView{FleetReport: report, Proposals: proposals})
}

func (s *Server) handlePatterns(w http.ResponseWriter, r *http.Request) {
	views, err := s.store.Patterns(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if views == nil {
		views = []store.PatternView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": views})
}
