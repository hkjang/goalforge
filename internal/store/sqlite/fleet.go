package sqlite

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/goalforge/goalforge/internal/standards"
)

// CriterionAcross is one criterion's standing across every project that is
// held to it.
type CriterionAcross struct {
	StandardID, Title, Severity string
	// Counted only across projects the criterion actually applies to. A
	// criterion out of profile somewhere is not a gap there, and folding those
	// in would make every conditional criterion look half-met forever.
	Applicable                   int
	Met, Unmet, Partial, Unknown int
	Excepted                     int
	ExceptedIn, UnmetIn          []string
}

// ExceptionRate is the share of applicable projects that have excused this
// criterion.
func (c CriterionAcross) ExceptionRate() float64 {
	if c.Applicable == 0 {
		return 0
	}
	return float64(c.Excepted) / float64(c.Applicable)
}

// FleetReport is the pack measured across every project pinned to it.
type FleetReport struct {
	PackRef  string
	Projects []string
	Criteria []CriterionAcross
}

// Fleet measures one pack across every project that pinned it.
func (s *Store) Fleet(ctx context.Context, pack standards.Pack, now time.Time) (FleetReport, error) {
	report := FleetReport{PackRef: pack.Ref()}
	projects, err := s.ListProjects(ctx)
	if err != nil {
		return report, err
	}
	across := map[string]*CriterionAcross{}
	for _, standard := range pack.Standards {
		across[standard.ID] = &CriterionAcross{StandardID: standard.ID, Title: standard.Title,
			Severity: standard.Severity}
	}
	for _, project := range projects {
		profile, _, profileErr := s.StandardProfile(ctx, project.ID)
		if errors.Is(profileErr, ErrNotFound) {
			// Not held to this pack at all. Counting it would dilute every
			// number with projects that never agreed to be measured.
			continue
		}
		if profileErr != nil {
			return report, profileErr
		}
		if profile.PackRef != pack.Ref() {
			continue
		}
		report.Projects = append(report.Projects, project.Name)
		views, viewErr := s.AssessmentsFor(ctx, project.ID, pack, profile, now)
		if viewErr != nil {
			return report, viewErr
		}
		for _, view := range views {
			entry, known := across[view.StandardID]
			if !known {
				continue
			}
			entry.Applicable++
			switch view.Result {
			case standards.ResultMet:
				entry.Met++
			case standards.ResultUnmet:
				entry.Unmet++
				entry.UnmetIn = append(entry.UnmetIn, project.Name)
			case standards.ResultPartial:
				entry.Partial++
			case standards.ResultNotApplicable:
				entry.Excepted++
				entry.ExceptedIn = append(entry.ExceptedIn, project.Name)
			default:
				entry.Unknown++
			}
		}
	}
	sort.Strings(report.Projects)
	for _, standard := range pack.Standards {
		entry := across[standard.ID]
		if entry.Applicable == 0 {
			// Applies to no project on this fleet. Listing it would fill the
			// comparison with rows that can never move.
			continue
		}
		report.Criteria = append(report.Criteria, *entry)
	}
	return report, nil
}

// PackProposal is a suggested change to the catalogue, with the evidence for
// it.
type PackProposal struct {
	StandardID, Title, Change, Evidence string
}

// exceptionMajority is the share of projects excusing a criterion at which the
// criterion itself becomes the thing worth re-reading.
//
// Set above half rather than at any exception because one project excusing a
// criterion is that project's circumstance, and two out of three is a small
// sample. A majority of a fleet is a pattern.
const exceptionMajority = 0.5

// ProposePackChanges reads the fleet for evidence that the catalogue, rather
// than the projects, is what needs changing.
//
// A criterion most of a fleet has excused is not a criterion most of a fleet
// is failing — it is one that does not fit the work these projects do. Leaving
// it in the pack means every new project inherits an exception to write, and
// the exceptions become a ritual rather than a decision.
func (s *Store) ProposePackChanges(ctx context.Context, pack standards.Pack, now time.Time) ([]PackProposal, error) {
	report, err := s.Fleet(ctx, pack, now)
	if err != nil {
		return nil, err
	}
	// Whether this fleet has shown it can settle anything at all. On a fleet
	// that has never been assessed every required criterion is unknown, and
	// flagging all of them would say the catalogue is wrong when what is
	// actually true is that nobody has run it yet — and the suggested fix
	// (attach a gate) is not a change to the catalogue in the first place.
	settling := false
	for _, criterion := range report.Criteria {
		if criterion.Met > 0 {
			settling = true
			break
		}
	}
	var proposals []PackProposal
	for _, criterion := range report.Criteria {
		if criterion.Applicable < 2 {
			// One project cannot show that a criterion is wrong, only that it
			// did not suit one project.
			continue
		}
		if criterion.ExceptionRate() > exceptionMajority {
			proposals = append(proposals, PackProposal{StandardID: criterion.StandardID, Title: criterion.Title,
				Change: "적용 조건을 좁히거나 기준을 다시 쓰세요",
				Evidence: fmt.Sprintf("적용 대상 %d개 중 %d개가 예외 처리했습니다 (%s) — 대부분이 예외를 쓰는 기준은 프로젝트가 아니라 기준을 다시 볼 신호입니다",
					criterion.Applicable, criterion.Excepted, joinNames(criterion.ExceptedIn))})
			continue
		}
		if settling && criterion.Severity == standards.SeverityRequired && criterion.Unknown == criterion.Applicable {
			proposals = append(proposals, PackProposal{StandardID: criterion.StandardID, Title: criterion.Title,
				Change: "정산할 게이트를 붙이거나 권장으로 낮추세요",
				Evidence: fmt.Sprintf("다른 기준들은 정산되는데 이 기준만 적용 대상 %d개 모두 미확인입니다 — 아무도 판정하지 못하는 필수 기준은 통과한 적 없이 모두를 막습니다",
					criterion.Applicable)})
		}
	}
	return proposals, nil
}

func joinNames(names []string) string {
	sort.Strings(names)
	if len(names) > 4 {
		return fmt.Sprintf("%s 외 %d개", joinComma(names[:4]), len(names)-4)
	}
	return joinComma(names)
}

func joinComma(values []string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += ", "
		}
		out += value
	}
	return out
}
