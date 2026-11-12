package service

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/utils"
	"yagp/pkg/gost"

	"google.golang.org/grpc/codes"
	"gorm.io/gorm"
)

type transparentTestTunnel struct {
	db                  *gorm.DB
	service             *TunnelService
	entry, exit         model.GostNode
	source, destination model.GostRule
	tunnel              model.GostTunnel
	entryNode, exitNode *redirectLifecycleNode
}

func newTransparentTestTunnel(t *testing.T) *transparentTestTunnel {
	t.Helper()
	db := newServiceTestDB(t)
	loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
	entryNode := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}, dockerAddress: "172.17.0.1", mark: 1234}
	exitNode := &redirectLifecycleNode{lifecycleNode: lifecycleNode{t: t}}
	entry, exit := startServiceTestNode(t, entryNode), startServiceTestNode(t, exitNode)
	entry.IPv4 = "192.0.2.15"
	exit.Name, exit.Address = "exit", "198.51.100.21"
	saveServiceFixtures(t, db, &entry, &exit)
	source := model.GostRule{Name: "source", NodeID: &entry.ID, ListenIP: "127.0.0.1", ListenPort: 12345, UseClashRuleset: true}
	destination := model.GostRule{Name: "destination", NodeID: &exit.ID, ListenPort: 18443}
	saveServiceFixtures(t, db, &source, &destination,
		&model.GostRuleset{ID: gostRulesetID, URL: "https://example.test/rules", Matchers: []string{".example.test"}})
	tunnel := model.GostTunnel{Name: "transparent", EntryNodeID: entry.ID, ExitNodeID: exit.ID,
		EntryRuleID: &source.ID, ExitRuleID: &destination.ID, RelaySecret: "secret", Protocol: "tcp", RelayPort: destination.ListenPort}
	saveServiceFixtures(t, db, &tunnel)
	return &transparentTestTunnel{db: db, service: NewTunnelService(db), entry: entry, exit: exit,
		source: source, destination: destination, tunnel: tunnel, entryNode: entryNode, exitNode: exitNode}
}

func (f *transparentTestTunnel) start() error {
	return f.service.Start(f.tunnel.ID, testAdmin, testIP, testAgent)
}

func (f *transparentTestTunnel) stop() error {
	return f.service.Stop(f.tunnel.ID, testAdmin, testIP, testAgent)
}

func (f *transparentTestTunnel) assertStopped(t *testing.T) {
	t.Helper()
	current, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	active, _, _ := f.entryNode.redirectStatus()
	if current.Status != model.TunnelStatusStopped || current.EntryRule.Status != model.RuleStatusStopped ||
		current.ExitRule.Status != model.RuleStatusStopped || active ||
		f.entryNode.service(entranceServiceName(f.source.ID)).Name != "" ||
		f.entryNode.service(dockerEntranceServiceName(f.source.ID)).Name != "" ||
		f.entryNode.chain(tunnelChainName(f.tunnel.ID)).Name != "" ||
		f.entryNode.chain(clashTunnelChainName(f.tunnel.ID)).Name != "" ||
		f.exitNode.service(relayServiceName(f.tunnel.ID)).Name != "" {
		t.Fatalf("stopped tunnel left an active redirect, Gost resources, or running state: %+v", current)
	}
}

func TestTransparentTunnelRejectsPartialSetupBeforeSaving(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fail     func(t *testing.T, f *transparentTestTunnel)
		want     error
		wantText string
	}{
		{"Docker listener", func(_ *testing.T, f *transparentTestTunnel) {
			f.entryNode.mu.Lock()
			defer f.entryNode.mu.Unlock()
			f.entryNode.rejectResource = dockerEntranceServiceName(f.source.ID)
		}, bizerrors.ErrRuleStartFailed, ""},
		{"Clash chain", func(_ *testing.T, f *transparentTestTunnel) {
			f.entryNode.mu.Lock()
			defer f.entryNode.mu.Unlock()
			f.entryNode.rejectResource = clashTunnelChainName(f.tunnel.ID)
		}, bizerrors.ErrRuleChainCreateFailed, ""},
		{"exit quota", func(t *testing.T, f *transparentTestTunnel) {
			requireServiceError(t, f.db.Model(&f.exit).Update("traffic_limit_bytes", 100).Error, nil)
		}, bizerrors.ErrNodeTrafficQuotaUnavailable, ""},
		{"Relay admission", func(t *testing.T, f *transparentTestTunnel) {
			_, err := f.service.UpdateAllowlist(t.Context(), f.tunnel.ID, []uint{f.entry.ID}, testAdmin, testIP, testAgent)
			requireServiceError(t, err, nil)
			f.exitNode.mu.Lock()
			defer f.exitNode.mu.Unlock()
			f.exitNode.reject = map[string]int{"POST /config/admissions": http.StatusBadGateway}
		}, nil, "写入 Gost 准入配置失败"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newTransparentTestTunnel(t)
			tc.fail(t, f)
			err := f.start()
			if tc.wantText == "" {
				requireServiceError(t, err, tc.want)
			} else if err == nil || !strings.Contains(err.Error(), tc.wantText) {
				t.Fatalf("start error = %v, want %q", err, tc.wantText)
			}
			f.assertStopped(t)
			entrySaves, _ := f.entryNode.savedService()
			exitSaves, _ := f.exitNode.savedService()
			if entrySaves != 0 || exitSaves != 0 {
				t.Fatalf("partial setup persisted Gost config: entry=%d exit=%d", entrySaves, exitSaves)
			}
		})
	}
}

func TestTransparentTunnelCanRetryStopAfterNodeFailures(t *testing.T) {
	f := newTransparentTestTunnel(t)
	requireServiceError(t, f.start(), nil)
	forward, docker := entranceServiceName(f.source.ID), dockerEntranceServiceName(f.source.ID)
	chain := clashTunnelChainName(f.tunnel.ID)

	f.entryNode.mu.Lock()
	f.entryNode.removeCode = codes.Unavailable
	f.entryNode.mu.Unlock()
	requireServiceError(t, f.stop(), bizerrors.ErrTunnelTransparentRedirect)
	requireServiceError(t, f.service.Delete(f.tunnel.ID, testAdmin, testIP, testAgent), bizerrors.ErrTunnelTransparentRedirect)
	active, _, _ := f.entryNode.redirectStatus()
	if !active || f.entryNode.service(forward).Name == "" || f.entryNode.service(docker).Name == "" || f.entryNode.chain(chain).Name == "" {
		t.Fatal("failed nftables removal disrupted the running tunnel")
	}

	f.entryNode.mu.Lock()
	f.entryNode.removeCode = codes.OK
	f.entryNode.reject = map[string]int{"DELETE /config/services/" + docker: http.StatusBadGateway}
	f.entryNode.mu.Unlock()
	if err := f.stop(); err == nil {
		t.Fatal("failed Docker listener deletion reported success")
	}
	active, _, _ = f.entryNode.redirectStatus()
	if active || f.entryNode.service(docker).Name == "" || f.entryNode.chain(chain).Name == "" {
		t.Fatal("partial stop must disable interception and retain the chain for a live listener")
	}

	f.entryNode.mu.Lock()
	f.entryNode.reject = nil
	f.entryNode.mu.Unlock()
	requireServiceError(t, f.stop(), nil)
	requireServiceError(t, f.stop(), nil)
	f.assertStopped(t)
}

func TestTransparentTunnelRollsBackFailedRuntimeTransactions(t *testing.T) {
	for _, endpoint := range []string{"source", "destination", "tunnel"} {
		t.Run(endpoint, func(t *testing.T) {
			f := newTransparentTestTunnel(t)
			table, id := "rules", f.source.ID
			switch endpoint {
			case "destination":
				id = f.destination.ID
			case "tunnel":
				table, id = "tunnels", f.tunnel.ID
			}
			rejectTransition := func(next string) {
				t.Helper()
				sql := fmt.Sprintf("CREATE TRIGGER reject_runtime BEFORE UPDATE OF status ON %s WHEN NEW.id = %d AND NEW.status = '%s' BEGIN SELECT RAISE(ABORT, 'test write rejected'); END", table, id, next)
				requireServiceError(t, f.db.Exec(sql).Error, nil)
			}
			allowTransition := func() {
				t.Helper()
				requireServiceError(t, f.db.Exec("DROP TRIGGER reject_runtime").Error, nil)
			}
			rejectTransition("running")
			requireWriteRejected(t, f.start())
			f.assertStopped(t)
			allowTransition()
			requireServiceError(t, f.start(), nil)

			rejectTransition("stopped")
			requireWriteRejected(t, f.stop())
			current, err := f.service.GetByID(f.tunnel.ID)
			requireServiceError(t, err, nil)
			if current.Status != model.TunnelStatusRunning || current.EntryRule.Status != model.RuleStatusRunning ||
				current.ExitRule.Status != model.RuleStatusRunning {
				t.Fatalf("partial database transition leaked: %+v", current)
			}
			allowTransition()
			requireServiceError(t, f.stop(), nil)
			f.assertStopped(t)
		})
	}
}

func TestEndpointTunnelDialsMappedIPAndRetainsLocalBind(t *testing.T) {
	f := newTransparentTestTunnel(t)
	const listenIP = "10.20.30.40"
	const firstConnectIP = "203.0.113.8"
	const nextConnectIP = "2001:db8::8"
	f.destination.ListenIP = listenIP
	requireServiceError(t, f.db.Model(&f.destination).Update("listen_ip", listenIP).Error, nil)
	f.exit.AddressMappings = []model.NodeAddressMapping{
		{ListenIP: "10.20.30.41", ConnectIP: "203.0.113.7"},
		{ListenIP: listenIP, ConnectIP: firstConnectIP},
	}
	requireServiceError(t, f.db.Model(&f.exit).Select("AddressMappings").Updates(&f.exit).Error, nil)
	requireServiceError(t, f.start(), nil)
	tunnel, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	sync := NewRuleSyncService(f.db)
	client := utils.GetGostClient(&f.entry)
	chainNames := []string{tunnel.ChainID, clashTunnelChainName(tunnel.ID)}
	checkHost := func(want string) {
		t.Helper()
		if got := f.exitNode.service(tunnel.ServiceID).Addr; got != listenIP+":18443" {
			t.Fatalf("relay listener = %q, want %s:18443", got, listenIP)
		}
		for _, name := range chainNames {
			address, ok := f.entryNode.chain(name).TunnelRelayAddress()
			if !ok || address != net.JoinHostPort(want, "18443") {
				t.Errorf("chain %s relay address = %q, want host %q at port 18443", name, address, want)
			}
		}
		active, _, redirectHost := f.entryNode.redirectStatus()
		if !active || redirectHost != want {
			t.Errorf("redirect active = %t, host = %q, want active with host %q", active, redirectHost, want)
		}
	}
	checkHost(firstConnectIP)

	for _, step := range []struct {
		mappings []model.NodeAddressMapping
		want     string
	}{
		{[]model.NodeAddressMapping{{ListenIP: listenIP, ConnectIP: nextConnectIP}}, nextConnectIP},
		{[]model.NodeAddressMapping{}, listenIP},
	} {
		f.exit.AddressMappings = step.mappings
		requireServiceError(t, f.db.Model(&f.exit).Select("AddressMappings").Updates(&f.exit).Error, nil)
		exit, err := repository.NewNodeRepository(f.db).FindByID(f.exit.ID)
		requireServiceError(t, err, nil)
		sync.syncTunnels(client, f.entry.ID, []model.GostTunnel{*tunnel},
			[]gost.ChainConfig{f.entryNode.chain(chainNames[0]), f.entryNode.chain(chainNames[1])}, map[uint]*model.GostNode{exit.ID: exit})
		checkHost(step.want)
	}
}

func TestTransparentRelaySyncWaitsForRedirectUpdate(t *testing.T) {
	f := newTransparentTestTunnel(t)
	requireServiceError(t, f.start(), nil)
	tunnel, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	chain := clashTunnelChainName(tunnel.ID)
	sync := NewRuleSyncService(f.db)
	client := utils.GetGostClient(&f.entry)
	const nextHost = "198.51.100.22"
	retarget := func(host string) {
		sync.syncTunnels(client, f.entry.ID, []model.GostTunnel{*tunnel},
			[]gost.ChainConfig{f.entryNode.chain(tunnel.ChainID), f.entryNode.chain(chain)}, map[uint]*model.GostNode{f.exit.ID: {Address: host}})
	}
	checkHost := func(want string) {
		t.Helper()
		address, _ := f.entryNode.chain(chain).TunnelRelayAddress()
		_, _, relayHost := f.entryNode.redirectStatus()
		if address != want+":18443" || relayHost != want {
			t.Fatalf("redirect and Gost relay addresses = %q and %q, want %q", relayHost, address, want)
		}
	}

	tunnel.Status = model.TunnelStatusStopped
	retarget(nextHost)
	checkHost(f.exit.Address)
	tunnel.Status = model.TunnelStatusRunning
	f.entryNode.mu.Lock()
	f.entryNode.prepareCode = codes.FailedPrecondition
	f.entryNode.mu.Unlock()
	retarget(nextHost)
	checkHost(f.exit.Address)
	f.entryNode.mu.Lock()
	f.entryNode.prepareCode, f.entryNode.rejectApply = codes.OK, true
	f.entryNode.mu.Unlock()
	retarget(nextHost)
	checkHost(f.exit.Address)
	f.entryNode.mu.Lock()
	f.entryNode.rejectApply = false
	f.entryNode.mu.Unlock()
	retarget(nextHost)
	checkHost(nextHost)

	// An existing manual transparent proxy can have its Gost chain retargeted
	// even when the node has no managed nftables state.
	f.entryNode.mu.Lock()
	f.entryNode.redirectActive = false
	f.entryNode.mu.Unlock()
	for _, older := range []struct {
		code codes.Code
		host string
	}{{codes.OK, "198.51.100.23"}, {codes.Unimplemented, "198.51.100.24"}} {
		f.entryNode.mu.Lock()
		f.entryNode.prepareCode = older.code
		f.entryNode.mu.Unlock()
		retarget(older.host)
		address, _ := f.entryNode.chain(chain).TunnelRelayAddress()
		active, _, relayHost := f.entryNode.redirectStatus()
		if active || relayHost != nextHost || address != older.host+":18443" {
			t.Fatalf("existing Gost chain sync unexpectedly enabled nftables: active=%t nft=%q chain=%q", active, relayHost, address)
		}
	}
	f.entryNode.mu.Lock()
	f.entryNode.removeCode = codes.Unimplemented
	f.entryNode.mu.Unlock()
	requireServiceError(t, f.stop(), nil)
	f.assertStopped(t)
}

func TestEndpointTunnelRepairsExistingRelayAdmissionOnRetry(t *testing.T) {
	f := newTransparentTestTunnel(t)
	_, err := f.service.UpdateAllowlist(t.Context(), f.tunnel.ID, []uint{f.entry.ID}, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	relay := relayServiceName(f.tunnel.ID)
	seedRelay := func() {
		t.Helper()
		created, err := utils.GetGostClient(&f.exit).EnsureService(&gost.ServiceConfig{
			Name: relay, Addr: ":18443", Listener: &gost.ListenerConfig{Type: "tcp"},
			Handler: &gost.HandlerConfig{Type: "relay"},
		})
		if err != nil || !created {
			t.Fatalf("seed existing relay: created=%v error=%v", created, err)
		}
	}
	seedRelay()
	f.exitNode.rejectCommand("PUT /config/services/"+relay, http.StatusServiceUnavailable)
	if err := f.start(); err == nil || !strings.Contains(err.Error(), "写入 Gost 准入配置失败") {
		t.Fatalf("start failed to report rejected Relay update: %v", err)
	}
	f.assertStopped(t)
	f.exitNode.rejectCommand("PUT /config/services/"+relay, 0)
	seedRelay()
	requireServiceError(t, f.start(), nil)
	if service := f.exitNode.service(relay); !service.HasAdmission(gost.TunnelRelayAllowlistName(relay)) {
		t.Fatalf("existing Relay did not receive the tunnel admission: %+v", service)
	}
}

func TestTunnelRejectsInvalidOrUnpersistedAllowlist(t *testing.T) {
	f := newTransparentTestTunnel(t)
	missing := f.exit.ID + 1
	update := &dto.UpdateTunnelReq{Name: "configured", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID, AllowlistNodeIDs: []uint{missing}}
	create := &dto.CreateTunnelReq{Name: "invalid", EntryRuleID: f.source.ID, ExitRuleID: f.destination.ID, AllowlistNodeIDs: []uint{missing}}
	if _, err := f.service.Create(create, testAdmin, testIP, testAgent); err == nil {
		t.Fatal("created tunnel with a missing allowlist node")
	}
	if _, err := f.service.Update(f.tunnel.ID, update, testAdmin, testIP, testAgent); err == nil {
		t.Fatal("updated tunnel with a missing allowlist node")
	}
	if _, err := f.service.UpdateAllowlist(t.Context(), f.tunnel.ID, update.AllowlistNodeIDs, testAdmin, testIP, testAgent); err == nil {
		t.Fatal("stored a missing allowlist node")
	}
	if _, err := f.service.UpdateAllowlist(t.Context(), f.tunnel.ID+1, []uint{f.entry.ID}, testAdmin, testIP, testAgent); err == nil {
		t.Fatal("updated allowlist for a missing tunnel")
	}
	update.AllowlistNodeIDs = []uint{f.entry.ID, f.entry.ID}
	update.RelayPort = 8443
	_, err := f.service.Update(f.tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointsRequired)
	update.RelayPort = 0
	update.ExitRuleID = f.source.ID
	_, err = f.service.Update(f.tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, bizerrors.ErrTunnelEndpointsRequired)
	update.ExitRuleID = f.destination.ID

	requireServiceError(t, f.db.Exec("CREATE TRIGGER reject_allowlist BEFORE UPDATE OF allowlist_node_ids ON tunnels BEGIN SELECT RAISE(ABORT, 'test write rejected'); END").Error, nil)
	_, err = f.service.UpdateAllowlist(t.Context(), f.tunnel.ID, update.AllowlistNodeIDs, testAdmin, testIP, testAgent)
	requireWriteRejected(t, err)
	current, err := f.service.GetByID(f.tunnel.ID)
	requireServiceError(t, err, nil)
	if current.Name != f.tunnel.Name || len(current.AllowlistNodeIDs) != 0 {
		t.Fatalf("rejected edits changed the stored tunnel: %+v", current)
	}
	requireServiceError(t, f.db.Exec("DROP TRIGGER reject_allowlist").Error, nil)
	current, err = f.service.Update(f.tunnel.ID, update, testAdmin, testIP, testAgent)
	requireServiceError(t, err, nil)
	if current.Name != update.Name || len(current.AllowlistNodeIDs) != 1 || current.AllowlistNodeIDs[0] != f.entry.ID {
		t.Fatalf("valid allowlist was not applied or deduplicated: %+v", current)
	}
}
