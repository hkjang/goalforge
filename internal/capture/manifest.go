// Package capture keeps the screenshots a project's documentation shows in
// step with the code they claim to depict.
//
// A screenshot is a claim about how the product looks and behaves right now.
// Taken once and left alone it goes on making that claim after the screen has
// changed, and a guide full of stale pictures is worse than one with none:
// the reader trusts it and is wrong.
package capture

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Page is one screen in one state, as the manifest declares it.
type Page struct {
	// Route is the path the capture visits, written the way the application
	// writes it.
	Route string `json:"route"`
	// Role is who is looking — anonymous, user, admin. The same route shows
	// different things to different people, and a guide that captures only the
	// admin view documents a product most of its readers do not have.
	Role string `json:"role"`
	// State is which representative condition this is: populated, empty,
	// loading, error, denied, mobile. A guide showing only the populated state
	// leaves every reader who hits an empty one thinking something broke.
	State string `json:"state"`
	// Script is the playwright-player script that reaches this state. A page
	// nobody can reach is not a page in the manifest; it is a wish.
	Script string `json:"script"`
	// Sources are the paths whose change would alter this screen. They are
	// what makes staleness a question the program can answer rather than a
	// date comparison that marks every capture stale after any commit.
	Sources []string `json:"sources"`
	// Required marks a page the documentation gate insists on.
	Required bool `json:"required"`
}

// Key identifies a page across captures. Route, role and state together are
// the screen; two of the three matching is a different screen.
func (p Page) Key() string {
	return strings.Join([]string{
		strings.TrimSpace(p.Route),
		strings.ToLower(strings.TrimSpace(p.Role)),
		strings.ToLower(strings.TrimSpace(p.State)),
	}, "|")
}

// Label is the page as a person refers to it.
func (p Page) Label() string {
	return fmt.Sprintf("%s [%s/%s]", p.Route, p.Role, p.State)
}

// Manifest is every screen the documentation depicts.
type Manifest struct {
	// BaseURL is where the captures are taken. It is recorded because a
	// screenshot from production carries whatever was on the screen at the
	// time, and what is on production is somebody's real data.
	BaseURL string `json:"base_url"`
	// SeedRef names the seed data the captures run against, so a reviewer can
	// tell whether the names in a screenshot are invented or real.
	SeedRef string `json:"seed_ref"`
	Pages   []Page `json:"pages"`
}

// Validate refuses a manifest that cannot be executed.
func (m Manifest) Validate() error {
	if strings.TrimSpace(m.BaseURL) == "" {
		return errors.New("캡처를 찍을 주소가 필요합니다")
	}
	if strings.TrimSpace(m.SeedRef) == "" {
		// Without it nobody can say whether the screenshots hold invented data
		// or somebody's real name, and that question is asked after the guide
		// is published, not before.
		return errors.New("시드 데이터를 지정해야 합니다 — 화면 속 이름이 지어낸 것인지 실제인지 말할 수 있어야 합니다")
	}
	if len(m.Pages) == 0 {
		return errors.New("캡처할 화면이 없습니다")
	}
	seen := map[string]bool{}
	for i, page := range m.Pages {
		switch {
		case strings.TrimSpace(page.Route) == "":
			return fmt.Errorf("%d번째 화면에 경로가 없습니다", i+1)
		case strings.TrimSpace(page.Role) == "" || strings.TrimSpace(page.State) == "":
			return fmt.Errorf("%s: 역할과 상태가 필요합니다 — 같은 경로가 보는 사람과 상황에 따라 다르게 보입니다", page.Route)
		case strings.TrimSpace(page.Script) == "":
			return fmt.Errorf("%s: 이 화면에 도달하는 스크립트가 없습니다 — 아무도 도달할 수 없는 화면은 매니페스트의 항목이 아니라 바람입니다", page.Label())
		case len(page.Sources) == 0:
			return fmt.Errorf("%s: 이 화면을 바꾸는 경로를 적어야 합니다 — 적지 않으면 캡처가 낡았는지 물을 수 없습니다", page.Label())
		}
		if seen[page.Key()] {
			return fmt.Errorf("%s: 같은 화면이 두 번 있습니다", page.Label())
		}
		seen[page.Key()] = true
	}
	return nil
}

// Required is the pages the documentation gate insists on.
func (m Manifest) Required() []Page {
	var required []Page
	for _, page := range m.Pages {
		if page.Required {
			required = append(required, page)
		}
	}
	sort.SliceStable(required, func(i, j int) bool { return required[i].Key() < required[j].Key() })
	return required
}

// Page finds a declared page by key.
func (m Manifest) Page(key string) (Page, bool) {
	for _, page := range m.Pages {
		if page.Key() == key {
			return page, true
		}
	}
	return Page{}, false
}
