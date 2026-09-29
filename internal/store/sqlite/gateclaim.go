package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/goalforge/goalforge/internal/standards"
)

// GateClaim is a gate saying which criteria it settles.
//
// The link has to be declared rather than inferred. Matching a gate to a
// criterion by kind alone would let any journey test settle every journey
// criterion — the same mistake as letting a build gate settle a journey one,
// moved up a level. Something green ran, and nobody checked it was about this.
type GateClaim struct {
	CheckType string `json:"check_type"`
	// Settles names the criteria a pass of this gate establishes.
	Settles []string `json:"settles"`
	// Produces names evidence kinds this gate yields beyond the one implied by
	// its kind — a journey runner that also captures screenshots, say. Without
	// it a criterion asking for a screenshot can never be settled by anything.
	Produces []string `json:"produces,omitempty"`
}

// SetGateClaim records what a gate settles, refusing criteria the pack does
// not contain.
//
// A claim naming a criterion that does not exist settles nothing and hides a
// typo that looks like coverage — the gate reports green, the criterion stays
// unknown, and nobody connects the two.
func (s *Store) SetGateClaim(ctx context.Context, projectID string, claim GateClaim, pack standards.Pack) error {
	if projectID == "" || strings.TrimSpace(claim.CheckType) == "" {
		return fmt.Errorf("project and check type are required")
	}
	normalized := make([]string, 0, len(claim.Settles))
	for _, id := range claim.Settles {
		id = strings.ToUpper(strings.TrimSpace(id))
		if id == "" {
			continue
		}
		if _, known := pack.Standard(id); !known {
			return fmt.Errorf("%s: no such criterion in %s", id, pack.Ref())
		}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	produces := make([]string, 0, len(claim.Produces))
	for _, kind := range claim.Produces {
		kind = strings.ToLower(strings.TrimSpace(kind))
		if kind == "" {
			continue
		}
		// A kind no criterion in the pack asks for is a typo, and a typo here
		// looks like coverage: the gate reports green, the evidence it claims
		// to produce settles nothing, and nobody connects the two.
		//
		// What a gate may *behave* as is not decided here — a build gate
		// claiming to produce a browser test result is caught at settlement,
		// where the gate's actual kind is known. This check is about spelling.
		if !evidenceKindInPack(pack, kind) {
			return fmt.Errorf("%s: %s 의 어떤 기준도 이 근거를 요구하지 않습니다", kind, pack.Ref())
		}
		produces = append(produces, kind)
	}
	sort.Strings(produces)
	settles, err := json.Marshal(normalized)
	if err != nil {
		return err
	}
	produced, err := json.Marshal(produces)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO gate_claims(project_id,check_type,settles,produces) VALUES(?,?,?,?)
ON CONFLICT(project_id,check_type) DO UPDATE SET settles=excluded.settles,produces=excluded.produces`,
		projectID, claim.CheckType, string(settles), string(produced))
	return err
}

// SetGateSettles is SetGateClaim for the common case of a gate that settles
// some criteria and produces only what its kind implies.
func (s *Store) SetGateSettles(ctx context.Context, projectID, checkType string, standardIDs []string, produces ...string) error {
	return s.SetGateClaim(ctx, projectID, GateClaim{CheckType: checkType, Settles: standardIDs, Produces: produces},
		standards.GoReactOfflineService())
}

// evidenceKindInPack reports whether any criterion asks for this kind.
func evidenceKindInPack(pack standards.Pack, kind string) bool {
	for _, standard := range pack.Standards {
		for _, required := range standard.EvidenceRequired {
			if strings.EqualFold(strings.TrimSpace(required), kind) {
				return true
			}
		}
	}
	return false
}

// GateClaims is every gate's claim for a project, keyed by check type.
func (s *Store) GateClaims(ctx context.Context, projectID string) (map[string]GateClaim, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,settles,produces FROM gate_claims WHERE project_id=?`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	claims := map[string]GateClaim{}
	for rows.Next() {
		var claim GateClaim
		var settles, produces string
		if err = rows.Scan(&claim.CheckType, &settles, &produces); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(settles), &claim.Settles); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(produces), &claim.Produces); err != nil {
			return nil, err
		}
		claims[claim.CheckType] = claim
	}
	return claims, rows.Err()
}

// LatestGateResults is the most recent result per check type for a goal, which
// is what a criterion's standing is read from.
func (s *Store) LatestGateResults(ctx context.Context, goalID string) (map[string]VerificationRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT check_type,status,actual_value,COALESCE(evidence_kind,''),COALESCE(output,''),required
FROM verification_results WHERE goal_id=? AND COALESCE(stale,0)=0 ORDER BY id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	latest := map[string]VerificationRecord{}
	for rows.Next() {
		var record VerificationRecord
		var required int
		if err = rows.Scan(&record.CheckType, &record.Status, &record.ActualValue, &record.EvidenceKind,
			&record.Output, &required); err != nil {
			return nil, err
		}
		record.Required = required == 1
		// Ordered oldest first, so the last write per check type wins. Stale
		// results are excluded by the query: evidence measured against a tree
		// that has since changed is not evidence about this one.
		latest[record.CheckType] = record
	}
	return latest, rows.Err()
}
