package handler

import (
	"fmt"
	"testing"
	"time"

	"yagp/internal/model"
)

func TestRuleListEnforcesItsAdvertisedPageSize(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	rules := make([]model.GostRule, 101)
	for i := range rules {
		rules[i] = model.GostRule{Name: fmt.Sprintf("rule-%d", i), Type: model.RuleTypeForward, ListenPort: 10000 + i, CreatedAt: time.Unix(int64(i+1), 0)}
	}
	if err := f.db.Create(&rules).Error; err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"pageSize=101", "pageSize=1000", "pageSize=-1", "pageSize=invalid", "page=-1"} {
		f.request(t, "GET", "/rules?"+query, "", 400)
	}
	first := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=100", "", 200))
	second := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=100&page=2", "", 200))
	if first.Total != 101 || first.PageSize != 100 || len(first.List) != 100 || second.PageSize != 100 || second.Page != 2 || len(second.List) != 1 || second.List[0].ID != rules[0].ID {
		t.Fatalf("page contract: first length=%d size=%d total=%d; second=%+v", len(first.List), first.PageSize, first.Total, second)
	}
	defaults := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules", "", 200))
	if defaults.Page != 1 || defaults.PageSize != 10 || defaults.Total != 101 || len(defaults.List) != 10 {
		t.Fatalf("default page metadata does not describe its rows: %+v", defaults)
	}
	last := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=1&page=101", "", 200))
	if last.Page != 101 || last.PageSize != 1 || last.Total != 101 || len(last.List) != 1 || last.List[0].ID != rules[0].ID {
		t.Fatalf("minimum page size skipped the final row: %+v", last)
	}
	empty := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=100&page=3", "", 200))
	if empty.Page != 3 || empty.PageSize != 100 || empty.Total != 101 || len(empty.List) != 0 {
		t.Fatalf("past-last page changed the total or page size: %+v", empty)
	}
}
