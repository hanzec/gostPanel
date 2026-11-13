package repository

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"yagp/internal/model"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openRepositoryDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "repository.db")), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	mustPersist(t, db.AutoMigrate(&model.GostNode{}, &model.GostRule{}, &model.GostTunnel{}, &model.ExitRelayHistory{}, &model.OperationLog{}, &model.SystemConfig{}))
	return db
}

func mustPersist(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestNodeRepositoryEnrollmentAndStaleReports(t *testing.T) {
	db := openRepositoryDB(t)
	nodes := NewNodeRepository(db)
	ctx := t.Context()
	manual := []string{"203.0.113.10", "2001:db8::10"}
	stale := model.GostNode{Name: "edge", Port: 39110, ManualAddresses: manual}
	mustPersist(t, nodes.Create(&stale))
	if err := nodes.SetRegistrationToken(ctx, 99, "unknown", time.Now().UTC().Add(time.Hour)); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("issuing token for absent node: %v", err)
	}
	mustPersist(t, nodes.SetRegistrationToken(ctx, stale.ID, "expired", time.Now().UTC().Add(-time.Hour)))
	enrolled := model.GostNode{
		ID: stale.ID, Username: "edge", Password: "first password",
		Address: "198.51.100.1", IPv4: "198.51.100.1",
	}
	credentials := model.NodeCredentials{AccessTokenHash: "access", RefreshTokenHash: "refresh"}
	if err := nodes.EnrollNode(ctx, &enrolled, "expired", credentials); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("consuming expired token: %v", err)
	}
	mustPersist(t, nodes.SetRegistrationToken(ctx, stale.ID, "first", time.Now().UTC().Add(time.Hour)))
	mustPersist(t, db.Exec(`CREATE TRIGGER fail_enrollment BEFORE UPDATE OF password ON nodes
		BEGIN SELECT RAISE(ABORT, 'enrollment storage failure'); END`).Error)
	if err := nodes.EnrollNode(ctx, &enrolled, "first", credentials); err == nil || errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("writing enrollment with broken storage: %v", err)
	}
	mustPersist(t, db.Exec("DROP TRIGGER fail_enrollment").Error)
	mustPersist(t, nodes.EnrollNode(ctx, &enrolled, "first", credentials))
	if err := nodes.EnrollNode(ctx, &enrolled, "first", credentials); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("replaying consumed token: %v", err)
	}
	mustPersist(t, nodes.UpdateStatus(stale.ID, model.NodeStatusOnline))
	mustPersist(t, nodes.UpdateLastCheck(stale.ID))
	legacyPort := stale.Port
	stale.Name, stale.Remark, stale.Port = "renamed", "saved after enrollment", 39111
	mustPersist(t, nodes.Update(&stale, nil, nil, nil, nil))
	current, err := nodes.FindByName("renamed")
	mustPersist(t, err)
	if current.Password != enrolled.Password || current.Status != model.NodeStatusOnline || current.LastCheckAt == nil ||
		current.RegistrationTokenHash != "" || current.RegistrationExpiresAt != nil || current.Port != legacyPort || current.Remark != stale.Remark || !slices.Equal(current.ManualAddresses, manual) {
		t.Fatalf("metadata update overwrote enrollment or health state: %+v", current)
	}

	supported := true
	mustPersist(t, nodes.UpdateIPs(ctx, current, "", "", nil, nil, supported))
	current, err = nodes.FindByID(current.ID)
	mustPersist(t, err)
	if !current.MPTCPSupported {
		t.Fatal("daemon capability was not saved before re-enrollment")
	}
	old := *current
	mustPersist(t, nodes.UpdateStatus(current.ID, model.NodeStatusOffline))
	mustPersist(t, nodes.SetRegistrationToken(ctx, current.ID, "second", time.Now().UTC().Add(time.Hour)))
	credentials.AccessTokenHash = "new access"
	mustPersist(t, nodes.EnrollNode(ctx, current, "second", credentials))
	if current.Password != old.Password || current.SessionGeneration != old.SessionGeneration+1 || current.AccessTokenHash != credentials.AccessTokenHash || !slices.Equal(current.ManualAddresses, manual) {
		t.Fatal("enrollment did not advance public credentials while preserving the Gost password")
	}
	if current.MPTCPSupported {
		t.Fatal("new enrollment retained the previous daemon's capability")
	}
	if err := nodes.UpdateIPs(ctx, &old, "", "2001:db8::99", nil, nil, supported); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("updating addresses with a superseded enrollment: %v", err)
	}
	mustPersist(t, nodes.UpdateIPs(ctx, current, "", "2001:db8::1", nil, nil, false))
	current, err = nodes.FindByID(current.ID)
	mustPersist(t, err)
	if current.Address != "198.51.100.1" || current.IPv6 != "2001:db8::1" {
		t.Fatalf("IPv6-only update displaced the IPv4 control address: %+v", current)
	}
	mustPersist(t, nodes.UpdateIPs(ctx, current, current.IPv4, current.IPv6, nil, nil, false))
	mustPersist(t, nodes.UpdateIPs(ctx, current, "198.51.100.2", "2001:db8::2", nil, nil, false))
	mustPersist(t, nodes.UpdateStats(current.ID, 3, 4))
	mustPersist(t, nodes.UpdateStats(current.ID, 5, 2))
	mustPersist(t, nodes.SetStats(ctx, current.ID, 2, 9))
	mustPersist(t, nodes.UpdateStats(current.ID, 1, 4))
	mustPersist(t, nodes.UpdateStatus(current.ID, model.NodeStatusOnline))
	online, err := nodes.GetAllOnline()
	mustPersist(t, err)
	if len(online) != 1 || online[0].ID != current.ID || online[0].Address != "198.51.100.2" || online[0].IPv6 != "2001:db8::2" ||
		online[0].InputBytes != 3 || online[0].OutputBytes != 13 || online[0].TotalBytes != 16 || !slices.Equal(online[0].ManualAddresses, manual) {
		t.Fatalf("online node state: %+v", online)
	}
}

func TestNodeRepositoryRejectsStaleQuotaSnapshots(t *testing.T) {
	nodes := NewNodeRepository(openRepositoryDB(t))
	node := model.GostNode{Name: "edge", Port: 39110, TrafficLimitBytes: 100, TrafficResetPeriod: "daily"}
	mustPersist(t, nodes.Create(&node))
	mustPersist(t, nodes.UpdateTrafficQuota(node, 24, 50, model.TrafficQuotaActive))
	current, err := nodes.FindByID(node.ID)
	mustPersist(t, err)
	if current.TrafficQuotaWindowStart != 24 || current.TrafficQuotaUsedBytes != 50 || current.TrafficQuotaState != model.TrafficQuotaActive {
		t.Fatalf("stored snapshot: %+v", current)
	}
	mustPersist(t, nodes.UpdateTrafficQuota(*current, 24, 50, model.TrafficQuotaActive))
	stale := *current
	limit, reset := int64(200), "weekly"
	mustPersist(t, nodes.Update(current, &limit, &reset, nil, nil))
	mustPersist(t, nodes.UpdateTrafficQuota(stale, 24, 100, model.TrafficQuotaBlocked))
	current, err = nodes.FindByID(node.ID)
	mustPersist(t, err)
	if current.TrafficLimitBytes != 200 || current.TrafficResetPeriod != "weekly" || current.TrafficQuotaState != model.TrafficQuotaPending || current.TrafficQuotaUsedBytes != 50 {
		t.Fatalf("a snapshot for the old policy overwrote the changed quota: %+v", current)
	}
}

func TestNodeRepositoryKeepsPrimaryIPv4InNewAddressSnapshots(t *testing.T) {
	nodes := NewNodeRepository(openRepositoryDB(t))
	ctx := t.Context()
	node := model.GostNode{Name: "edge"}
	mustPersist(t, nodes.Create(&node))
	mustPersist(t, nodes.UpdateIPs(ctx, &node, "", "2001:db8::1", []string{"2001:db8::1"}, nil, false))
	saved, err := nodes.FindByID(node.ID)
	mustPersist(t, err)
	if saved.Address != "2001:db8::1" || saved.IPv4 != "" {
		t.Fatalf("IPv6-only node did not keep an IPv6 primary address: %+v", saved)
	}
	mustPersist(t, nodes.UpdateIPs(ctx, &node, "192.0.2.1", "", []string{"192.0.2.1"}, nil, false))
	saved, err = nodes.FindByID(node.ID)
	mustPersist(t, err)
	if saved.IPv6 != "" || !slices.Equal(saved.Addresses, []string{"192.0.2.1"}) {
		t.Fatalf("new address snapshot did not clear IPv6: %+v", saved)
	}
	mustPersist(t, nodes.UpdateIPs(ctx, &node, "", "2001:db8::2", []string{"2001:db8::2"}, nil, false))
	saved, err = nodes.FindByID(node.ID)
	mustPersist(t, err)
	if saved.Address != "192.0.2.1" || saved.IPv4 != "192.0.2.1" || saved.IPv6 != "2001:db8::2" ||
		!slices.Equal(saved.Addresses, []string{"192.0.2.1", "2001:db8::2"}) {
		t.Fatalf("an IPv6-only snapshot erased the known IPv4 address: %+v", saved)
	}
}

func TestRepositoryForwardingRelationsAndScopedUpdates(t *testing.T) {
	db := openRepositoryDB(t)
	nodes, tunnels, rules := NewNodeRepository(db), NewTunnelRepository(db), NewRuleRepository(db)
	entry := model.GostNode{Name: "entry", Port: 39110}
	exit := model.GostNode{Name: "exit", Port: 39111}
	third := model.GostNode{Name: "third", Port: 39112}
	for _, node := range []*model.GostNode{&entry, &exit, &third} {
		mustPersist(t, nodes.Create(node))
	}
	tunnel := model.GostTunnel{Name: "relay", EntryNodeID: entry.ID, ExitNodeID: exit.ID}
	other := model.GostTunnel{Name: "other relay", EntryNodeID: third.ID, ExitNodeID: exit.ID}
	mustPersist(t, tunnels.Create(&tunnel))
	mustPersist(t, tunnels.Create(&other))
	tunnel.Remark = "persisted"
	mustPersist(t, tunnels.Update(&tunnel, false))
	mustPersist(t, tunnels.UpdateServiceInfo(tunnel.ID, "relay-service", "relay-chain"))
	mustPersist(t, tunnels.UpdateStatus(tunnel.ID, model.TunnelStatusRunning))
	mustPersist(t, tunnels.UpdateStatus(other.ID, model.TunnelStatusRunning))

	direct := model.GostRule{Name: "direct", NodeID: &entry.ID, Type: model.RuleTypeForward, ListenIP: "127.0.0.1", ListenPort: 9000}
	proxied := model.GostRule{Name: "proxied", TunnelID: &tunnel.ID, Type: model.RuleTypeTunnel, ListenPort: 9001}
	otherDirect := model.GostRule{Name: "other direct", NodeID: &third.ID, Type: model.RuleTypeForward, ListenPort: 9000}
	for _, rule := range []*model.GostRule{&direct, &proxied, &otherDirect} {
		mustPersist(t, rules.Create(rule))
		mustPersist(t, rules.UpdateStatus(rule.ID, model.RuleStatusRunning))
	}
	proxied.Targets = []string{"127.0.0.1:8080"}
	proxied.Status = model.RuleStatusRunning
	mustPersist(t, rules.Update(&proxied))
	mustPersist(t, rules.UpdateServiceID(proxied.ID, "proxied-service"))
	mustPersist(t, rules.UpdateObserverID(proxied.ID, "observer-global"))

	sample := model.ObserverCounters{InputBytes: 3, OutputBytes: 6, Connections: 2}
	mustPersist(t, rules.UpdateTrafficSample(proxied.ID, sample, sample))
	mustPersist(t, rules.UpdateTrafficSample(proxied.ID, model.ObserverCounters{}, model.ObserverCounters{}))
	stored, err := rules.FindByID(proxied.ID)
	mustPersist(t, err)
	if stored.ServiceID != "proxied-service" || stored.ObserverID != "observer-global" || !slices.Equal(stored.Targets, proxied.Targets) ||
		stored.Tunnel == nil || stored.Tunnel.EntryNode == nil || stored.Tunnel.EntryNode.ID != entry.ID ||
		stored.Tunnel.ExitNode == nil || stored.Tunnel.ExitNode.ID != exit.ID || stored.TotalBytes != 9 ||
		stored.TotalRequests != 2 || stored.ObserverCounters != (model.ObserverCounters{}) {
		t.Fatalf("rule details and related nodes: %+v", stored)
	}
	tunnelDetail, err := tunnels.FindByID(tunnel.ID)
	mustPersist(t, err)
	if tunnelDetail.EntryNode == nil || tunnelDetail.EntryNode.ID != entry.ID || tunnelDetail.ExitNode == nil ||
		tunnelDetail.ExitNode.ID != exit.ID || tunnelDetail.Remark != "persisted" || tunnelDetail.ChainID != "relay-chain" || tunnelDetail.ServiceID != "relay-service" {
		t.Fatalf("related tunnel details: %+v", tunnelDetail)
	}
	entryDetail, err := nodes.FindByIDWithRelations(entry.ID)
	mustPersist(t, err)
	if len(entryDetail.Rules) != 1 || entryDetail.Rules[0].ID != direct.ID || len(entryDetail.EntryTunnels) != 1 ||
		entryDetail.EntryTunnels[0].ID != tunnel.ID || len(entryDetail.ExitTunnels) != 0 {
		t.Fatalf("related node details: %+v", entryDetail)
	}
	byNode, err := rules.FindByNodeID(entry.ID)
	mustPersist(t, err)
	byTunnel, err := rules.FindByTunnelID(tunnel.ID)
	mustPersist(t, err)
	byExit, err := tunnels.FindByNodeID(exit.ID)
	mustPersist(t, err)
	if len(byNode) != 1 || byNode[0].ID != direct.ID || len(byTunnel) != 1 || byTunnel[0].ID != proxied.ID || len(byExit) != 2 {
		t.Fatalf("filters: by node %+v, by tunnel %+v, by exit %+v", byNode, byTunnel, byExit)
	}
	for _, tc := range []struct {
		protocol model.RuleProtocol
		ip       string
		port     int
		want     bool
	}{
		{model.RuleProtocolTCP, "", direct.ListenPort, true},
		{model.RuleProtocolTCP, direct.ListenIP, direct.ListenPort, true},
		{model.RuleProtocolTCP, "192.0.2.3", direct.ListenPort, false},
		{model.RuleProtocolUDP, direct.ListenIP, direct.ListenPort, false},
		{model.RuleProtocolTCP, "192.0.2.3", proxied.ListenPort, true},
	} {
		used, err := rules.ListenerConflicts(entry.ID, tc.protocol, tc.ip, tc.port)
		mustPersist(t, err)
		if used != tc.want {
			t.Fatalf("entry %s %s:%d conflict = %v, want %v", tc.protocol, tc.ip, tc.port, used, tc.want)
		}
	}
	excluded, err := rules.ListenerConflicts(entry.ID, model.RuleProtocolTCP, direct.ListenIP, direct.ListenPort, direct.ID)
	mustPersist(t, err)
	if excluded {
		t.Fatalf("entry port %d conflicts with itself", direct.ListenPort)
	}
	hasRules, err := tunnels.HasRules(tunnel.ID)
	mustPersist(t, err)
	if !hasRules {
		t.Fatal("tunnel reference was not detected")
	}

	mustPersist(t, rules.StopByNodeID(entry.ID))
	mustPersist(t, rules.StopByTunnelIDs(nil))
	mustPersist(t, rules.StopByTunnelIDs([]uint{tunnel.ID}))
	mustPersist(t, tunnels.StopByNodeID(entry.ID))
	runningRules, err := rules.CountByStatus(model.RuleStatusRunning)
	mustPersist(t, err)
	runningTunnels, err := tunnels.CountByStatus(model.TunnelStatusRunning)
	mustPersist(t, err)
	active, err := rules.FindByID(otherDirect.ID)
	mustPersist(t, err)
	otherTunnel, err := tunnels.FindByID(other.ID)
	mustPersist(t, err)
	if runningRules != 1 || runningTunnels != 1 || active.Status != model.RuleStatusRunning || otherTunnel.Status != model.TunnelStatusRunning {
		t.Fatalf("scoped stop affected unrelated records: %d rules, %d tunnels, rule %s, tunnel %s", runningRules, runningTunnels, active.Status, otherTunnel.Status)
	}
	mustPersist(t, rules.Delete(proxied.ID))
	if _, err := rules.FindByID(proxied.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("soft-deleted rule is still visible: %v", err)
	}
	hasRules, err = tunnels.HasRulesUsingTunnel(tunnel.ID)
	mustPersist(t, err)
	if hasRules {
		t.Fatal("deleted rule still blocks tunnel removal")
	}
	mustPersist(t, tunnels.Delete(tunnel.ID))
	if _, err := tunnels.FindByID(tunnel.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("soft-deleted tunnel is still visible: %v", err)
	}
	count, err := tunnels.CountAll()
	mustPersist(t, err)
	allRules, err := rules.CountAll()
	mustPersist(t, err)
	entryRules, err := rules.CountByNodeID(entry.ID)
	mustPersist(t, err)
	tunnelRules, err := rules.CountByType(model.RuleTypeTunnel)
	mustPersist(t, err)
	if count != 1 || allRules != 2 || entryRules != 1 || tunnelRules != 0 {
		t.Fatalf("counts after removal: %d tunnels, %d total rules, %d entry rules, %d tunnel rules", count, allRules, entryRules, tunnelRules)
	}
}

func TestRepositoryListPaginationAndQueryErrors(t *testing.T) {
	db := openRepositoryDB(t)
	nodes, tunnels, rules, logs := NewNodeRepository(db), NewTunnelRepository(db), NewRuleRepository(db), NewOperationLogRepository(db)
	old := model.GostNode{Name: "alpha", Port: 39110, CreatedAt: time.Unix(1, 0), Status: model.NodeStatusOnline}
	newer := model.GostNode{Name: "beta", Port: 39111, CreatedAt: time.Unix(2, 0), Status: model.NodeStatusOnline}
	offline := model.GostNode{Name: "offline", Port: 39112}
	for _, node := range []*model.GostNode{&old, &newer, &offline} {
		mustPersist(t, nodes.Create(node))
	}
	exists, err := nodes.ExistsByName("alpha")
	mustPersist(t, err)
	excluding, err := nodes.ExistsByName("alpha", old.ID)
	mustPersist(t, err)
	if !exists || excluding {
		t.Fatalf("node name uniqueness: exists %t, excluding owner %t", exists, excluding)
	}
	tunnel := model.GostTunnel{Name: "relay", EntryNodeID: old.ID, ExitNodeID: newer.ID}
	mustPersist(t, tunnels.Create(&tunnel))
	rule := model.GostRule{Name: "direct", NodeID: &old.ID, ListenPort: 9000}
	mustPersist(t, rules.Create(&rule))
	for _, log := range []model.OperationLog{
		{Username: "alice", Action: model.ActionCreate, ResourceType: model.ResourceTypeNode, ResourceID: old.ID, CreatedAt: time.Unix(1, 0)},
		{Username: "alice", Action: model.ActionUpdate, ResourceType: model.ResourceTypeNode, ResourceID: old.ID, CreatedAt: time.Unix(2, 0)},
		{Username: "bob", Action: model.ActionCreate, ResourceType: model.ResourceTypeNode, ResourceID: newer.ID, CreatedAt: time.Unix(3, 0)},
	} {
		mustPersist(t, logs.Create(&log))
	}
	page, total, err := nodes.List(&QueryOption{
		Conditions: map[string]any{"status = ?": model.NodeStatusOnline, "port BETWEEN ? AND ?": []any{39110, 39111}},
		Orders:     []string{"name ASC"},
		Preloads:   []string{"Rules"},
		Pagination: &Pagination{Page: 2, PageSize: 1},
	}, "")
	mustPersist(t, err)
	if total != 2 || len(page) != 1 || page[0].ID != newer.ID {
		t.Fatalf("filtered pagination should count matches before LIMIT: %+v, %d", page, total)
	}
	page, _, err = nodes.List(&QueryOption{Preloads: []string{"Rules"}, Orders: []string{"id ASC"}}, "")
	mustPersist(t, err)
	if len(page) != 3 || page[0].ID != old.ID || len(page[0].Rules) != 1 || page[0].Rules[0].ID != rule.ID {
		t.Fatalf("explicit preloads: %+v", page)
	}
	defaults := &Pagination{}
	recent, logCount, err := logs.List(&QueryOption{Conditions: map[string]any{"username = ?": "alice"}, Pagination: defaults})
	mustPersist(t, err)
	if defaults.Page != 1 || defaults.PageSize != 10 || logCount != 2 || len(recent) != 2 || recent[0].Action != model.ActionUpdate {
		t.Fatalf("default pagination and log ordering: %+v, %d, %+v", defaults, logCount, recent)
	}
	clamped := &Pagination{Page: 1, PageSize: 1000}
	_, _, err = nodes.List(&QueryOption{Pagination: clamped}, "")
	mustPersist(t, err)
	if clamped.PageSize != 100 {
		t.Fatalf("maximum page size was not enforced: %+v", clamped)
	}
	rulePage, ruleTotal, err := rules.List(nil)
	mustPersist(t, err)
	tunnelPage, tunnelTotal, err := tunnels.List(nil)
	mustPersist(t, err)
	if ruleTotal != 1 || len(rulePage) != 1 || rulePage[0].Node == nil || rulePage[0].Node.ID != old.ID ||
		tunnelTotal != 1 || len(tunnelPage) != 1 || tunnelPage[0].EntryNode == nil || tunnelPage[0].EntryNode.ID != old.ID {
		t.Fatalf("default lists did not preload relations: %+v and %+v", rulePage, tunnelPage)
	}
	mustPersist(t, nodes.Delete(offline.ID))
	_, err = nodes.FindByID(offline.ID)
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("deleted node still visible: %v", err)
	}
	all, err := nodes.CountAll()
	mustPersist(t, err)
	online, err := nodes.CountByStatus(model.NodeStatusOnline)
	mustPersist(t, err)
	if all != 2 || online != 2 {
		t.Fatalf("node counts after removal: total %d, online %d", all, online)
	}

	for _, opt := range []*QueryOption{
		{Conditions: map[string]any{"unknown_column = ?": "bad filter"}},
		{Preloads: []string{"UnknownRelation"}},
	} {
		if _, _, err := nodes.List(opt, ""); err == nil {
			t.Fatalf("node list silently ignored a query error: %+v", opt)
		}
		if _, _, err := rules.List(opt); err == nil {
			t.Fatalf("rule list silently ignored a query error: %+v", opt)
		}
		if _, _, err := tunnels.List(opt); err == nil {
			t.Fatalf("tunnel list silently ignored a query error: %+v", opt)
		}
		if _, _, err := logs.List(opt); err == nil {
			t.Fatalf("log list silently ignored a query error: %+v", opt)
		}
	}
	if _, err := nodes.FindByName("missing"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("absent node by name: %v", err)
	}
	if _, err := nodes.FindByIDWithRelations(offline.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("absent node with relations: %v", err)
	}
	mustPersist(t, db.Migrator().DropTable(&model.GostNode{}, &model.GostRule{}))
	if _, err := nodes.ExistsByName("alpha"); err == nil {
		t.Fatal("node uniqueness check hid storage failure")
	}
	if _, err := rules.ListenerConflicts(old.ID, model.RuleProtocolTCP, "", 9000); err == nil {
		t.Fatal("port conflict check hid storage failure")
	}
}

func TestSystemConfigRepositoryInitializesAndUpdatesSingleton(t *testing.T) {
	db := openRepositoryDB(t)
	configs := NewSystemConfigRepository(db)
	mustPersist(t, db.Exec(`CREATE TRIGGER fail_default_config BEFORE INSERT ON system_configs
		BEGIN SELECT RAISE(ABORT, 'system config storage failure'); END`).Error)
	if _, err := configs.Get(); err == nil {
		t.Fatal("config initialization hid write failure")
	}
	mustPersist(t, db.Exec("DROP TRIGGER fail_default_config").Error)
	first, err := configs.Get()
	mustPersist(t, err)
	if first.ID != 1 || first.SiteTitle != "yagp" || first.LogRetentionDays != 7 {
		t.Fatalf("default system configuration: %+v", first)
	}
	first.ID, first.SiteTitle, first.LogRetentionDays = 7, "Relays", 30
	mustPersist(t, configs.Update(first))
	current, err := configs.Get()
	mustPersist(t, err)
	if current.ID != 1 || current.SiteTitle != "Relays" || current.LogRetentionDays != 30 {
		t.Fatalf("updated system configuration: %+v", current)
	}
	mustPersist(t, db.Migrator().DropTable(&model.SystemConfig{}))
	if _, err := configs.Get(); err == nil {
		t.Fatal("config read hid storage failure")
	}
}
