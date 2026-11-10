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
	for _, size := range []int{101, 1000} {
		f.request(t, "GET", fmt.Sprintf("/rules?pageSize=%d", size), "", 400)
	}
	first := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=100", "", 200))
	second := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", "/rules?pageSize=100&page=2", "", 200))
	if first.Total != 101 || first.PageSize != 100 || len(first.List) != 100 || second.PageSize != 100 || second.Page != 2 || len(second.List) != 1 || second.List[0].ID != rules[0].ID {
		t.Fatalf("page contract: first length=%d size=%d total=%d; second=%+v", len(first.List), first.PageSize, first.Total, second)
	}
}
