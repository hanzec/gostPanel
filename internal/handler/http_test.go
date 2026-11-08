package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagp/internal/config"
	"yagp/internal/model"
	"yagp/internal/repository"
	"yagp/internal/service"
	"yagp/pkg/logger"
	"yagp/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type handlerHTTPFixture struct {
	engine *gin.Engine
	db     *gorm.DB
}

func newHandlerHTTPFixture(t *testing.T) *handlerHTTPFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if err := logger.Init(&logger.Config{Level: "error"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("host: panel.example\nreport:\n  listen: 127.0.0.1:39102\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(configPath); err != nil {
		t.Fatal(err)
	}
	db, err := gorm.Open(sqlite.Open(filepath.Join(dir, "panel.db")), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.GostNode{}, &model.GostRule{}, &model.GostTunnel{}, &model.OperationLog{}, &model.SystemConfig{}, &model.GostAllowlist{}, &model.GostRuleset{}); err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		c.Set("username", "test-admin")
		c.Next()
	})
	return &handlerHTTPFixture{engine: engine, db: db}
}

type handlerReply struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (f *handlerHTTPFixture) request(t *testing.T, method, path, body string, wantStatus int) handlerReply {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.5:1234"
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "yagp-handler-test")
	rec := httptest.NewRecorder()
	f.engine.ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("%s %s = HTTP %d, want %d: %s", method, path, rec.Code, wantStatus, rec.Body)
	}
	var reply handlerReply
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("%s %s invalid response: %v", method, path, err)
	}
	if (reply.Code == response.CodeSuccess) != (wantStatus < http.StatusBadRequest) {
		t.Fatalf("%s %s inconsistent response: %+v", method, path, reply)
	}
	return reply
}

func handlerData[T any](t *testing.T, reply handlerReply) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(reply.Data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

type handlerPage[T any] struct {
	List     []T   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

func (f *handlerHTTPFixture) registerNodes() {
	h := NewNodeHandler(service.NewNodeService(f.db, ""), service.NewNodeUpgradeService(f.db))
	f.engine.GET("/nodes", h.List)
	f.engine.POST("/nodes", h.Create)
	f.engine.GET("/nodes/:id", h.GetByID)
	f.engine.PUT("/nodes/:id", h.Update)
	f.engine.DELETE("/nodes/:id", h.Delete)
	f.engine.GET("/nodes/:id/config", h.GetConfig)
	f.engine.POST("/nodes/:id/registration-token", h.IssueRegistrationToken)
	f.engine.GET("/nodes/:id/gost/service", h.GostServiceStatus)
	f.engine.POST("/nodes/:id/gost/restart", h.RestartGostService)
	f.engine.GET("/nodes/:id/gost/logs", h.GostLogs)
	f.engine.GET("/nodes/:id/nft", h.GetNftConfig)
	f.engine.PUT("/nodes/:id/nft", h.ApplyNftConfig)
	f.engine.GET("/nodes/:id/upgrade", h.GetUpgrade)
	f.engine.PUT("/nodes/:id/upgrade", h.SetUpgradePolicy)
	f.engine.POST("/nodes/:id/upgrade/:component", h.StartUpgrade)
}

func (f *handlerHTTPFixture) registerForwarding() {
	rules := NewRuleHandler(service.NewRuleService(f.db))
	f.engine.GET("/rules", rules.List)
	f.engine.POST("/rules", rules.Create)
	f.engine.GET("/rules/:id", rules.GetByID)
	f.engine.PUT("/rules/:id", rules.Update)
	f.engine.DELETE("/rules/:id", rules.Delete)
	f.engine.POST("/rules/:id/start", rules.Start)
	f.engine.POST("/rules/:id/stop", rules.Stop)
	tunnels := NewTunnelHandler(service.NewTunnelService(f.db))
	f.engine.GET("/tunnels", tunnels.List)
	f.engine.POST("/tunnels", tunnels.Create)
	f.engine.GET("/tunnels/:id", tunnels.GetByID)
	f.engine.PUT("/tunnels/:id", tunnels.Update)
	f.engine.DELETE("/tunnels/:id", tunnels.Delete)
	f.engine.POST("/tunnels/:id/start", tunnels.Start)
	f.engine.POST("/tunnels/:id/stop", tunnels.Stop)
}

func (f *handlerHTTPFixture) registerSettings() {
	sys := NewSystemConfigHandler(service.NewSystemConfigService(repository.NewSystemConfigRepository(f.db)), service.NewBackupService(f.db))
	f.engine.GET("/system/config", sys.GetConfig)
	f.engine.GET("/system/public-config", sys.GetPublicConfig)
	f.engine.PUT("/system/config", sys.UpdateConfig)
	f.engine.POST("/system/email/test", sys.TestEmail)
	f.engine.POST("/system/backup", sys.Backup)
	allowlist := NewGostAllowlistHandler(service.NewGostAllowlistService(f.db))
	f.engine.GET("/system/gost-allowlist", allowlist.Get)
	f.engine.PUT("/system/gost-allowlist", allowlist.Update)
	ruleset := NewGostRulesetHandler(service.NewGostRulesetService(f.db))
	f.engine.GET("/system/gost-ruleset", ruleset.Get)
	f.engine.PUT("/system/gost-ruleset", ruleset.Update)
	f.engine.GET("/dashboard/stats", NewStatsHandler(service.NewStatsService(f.db)).GetDashboard)
	f.engine.GET("/logs", NewLogHandler(service.NewLogService(f.db)).List)
	f.engine.GET("/auth/info", CurrentAdmin)
}

func TestNodeHTTPInventoryLifecycle(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerSettings()
	const create = `{"name":"edge alpha","traffic_limit_bytes":4096,"traffic_reset_period":"daily"}`
	node := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", create, 200))
	path := fmt.Sprintf("/nodes/%d", node.ID)
	if node.ID == 0 || node.TrafficLimitBytes != 4096 || node.TrafficResetPeriod != "daily" {
		t.Fatalf("created node: %+v", node)
	}
	f.request(t, "POST", "/nodes", create, 400)
	f.request(t, "POST", "/nodes", `{"name":"invalid quota","traffic_limit_bytes":-1}`, 400)
	updated := handlerData[model.GostNode](t, f.request(t, "PUT", path, `{"name":"edge beta","remark":"primary"}`, 200))
	if updated.Name != "edge beta" || updated.TrafficLimitBytes != 4096 || updated.TrafficResetPeriod != "daily" {
		t.Fatalf("updating metadata discarded quota or new settings: %+v", updated)
	}
	list := handlerData[handlerPage[model.GostNode]](t, f.request(t, "GET", "/nodes?keyword=beta&pageSize=1&status=offline", "", 200))
	if list.Total != 1 || list.Page != 1 || list.PageSize != 1 || len(list.List) != 1 || list.List[0].ID != node.ID {
		t.Fatalf("filtered node page: %+v", list)
	}
	reply := f.request(t, "GET", path, "", 200)
	if detail := handlerData[model.GostNode](t, reply); detail.Remark != "primary" {
		t.Fatalf("node detail: %+v", detail)
	}
	if strings.Contains(string(reply.Data), "password") || strings.Contains(string(reply.Data), "registration_token") {
		t.Fatalf("node detail leaks credentials: %s", reply.Data)
	}
	issued := handlerData[struct {
		Token string `json:"token"`
	}](t, f.request(t, "POST", path+"/registration-token", "", 200))
	if !strings.HasPrefix(issued.Token, fmt.Sprintf("%d.", node.ID)) {
		t.Fatal("registration token does not belong to the node")
	}
	logs := handlerData[handlerPage[model.OperationLog]](t, f.request(t, "GET", "/logs?username=test-admin&action=update&resource_type=node&pageSize=1", "", 200))
	if logs.Total == 0 || len(logs.List) != 1 || logs.List[0].ResourceID != node.ID || logs.List[0].Username != "test-admin" ||
		logs.List[0].IPAddress != "192.0.2.5" || logs.List[0].UserAgent != "yagp-handler-test" {
		t.Fatalf("node audit log: %+v", logs)
	}
	f.request(t, "DELETE", path, "", 200)
	f.request(t, "GET", path, "", 404)
	f.request(t, "DELETE", path, "", 404)
	f.request(t, "POST", path+"/registration-token", "", 404)
	f.request(t, "PUT", path, create, 404)
}

func TestForwardingHTTPInventoryAndOfflineNodes(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerForwarding()
	f.registerSettings()
	entry := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"entry","port":39110}`, 200))
	exit := handlerData[model.GostNode](t, f.request(t, "POST", "/nodes", `{"name":"exit","port":39111}`, 200))

	tunnelBody := fmt.Sprintf(`{"name":"Tokyo","entry_node_id":%d,"exit_node_id":%d,"protocol":"tcp","relay_port":8081}`, entry.ID, exit.ID)
	tunnel := handlerData[model.GostTunnel](t, f.request(t, "POST", "/tunnels", tunnelBody, 200))
	tunnelPath := fmt.Sprintf("/tunnels/%d", tunnel.ID)
	updateTunnel := `{"name":"Tokyo UDP","protocol":"udp","relay_port":8082}`
	changed := handlerData[model.GostTunnel](t, f.request(t, "PUT", tunnelPath, updateTunnel, 200))
	if changed.Protocol != "udp" || changed.RelayPort != 8082 || changed.EntryNodeID != entry.ID || changed.ExitNodeID != exit.ID {
		t.Fatalf("updated tunnel: %+v", changed)
	}
	invalidTunnel := fmt.Sprintf(`{"name":"loop","entry_node_id":%d,"exit_node_id":%d,"protocol":"tcp","relay_port":8081}`, entry.ID, entry.ID)
	f.request(t, "POST", "/tunnels", invalidTunnel, 400)
	if detail := handlerData[model.GostTunnel](t, f.request(t, "GET", tunnelPath, "", 200)); detail.Name != "Tokyo UDP" {
		t.Fatalf("tunnel detail: %+v", detail)
	}
	tunnels := handlerData[handlerPage[model.GostTunnel]](t, f.request(t, "GET", fmt.Sprintf("/tunnels?node_id=%d&keyword=Tokyo&status=stopped", entry.ID), "", 200))
	if tunnels.Total != 1 || len(tunnels.List) != 1 || tunnels.List[0].ID != tunnel.ID {
		t.Fatalf("filtered tunnel list: %+v", tunnels)
	}
	if reply := f.request(t, "POST", tunnelPath+"/start", "", 400); reply.Code != 10216 {
		t.Fatalf("starting a tunnel on an offline entry: %+v", reply)
	}
	f.request(t, "POST", tunnelPath+"/stop", "", 200)

	ruleBody := fmt.Sprintf(`{"name":"Web","type":"tunnel","tunnel_id":%d,"protocol":"tcp","listen_port":9100,"targets":["127.0.0.1:9000"]}`, tunnel.ID)
	rule := handlerData[model.GostRule](t, f.request(t, "POST", "/rules", ruleBody, 200))
	rulePath := fmt.Sprintf("/rules/%d", rule.ID)
	updateRule := `{"name":"Web UDP","protocol":"udp","listen_port":9101,"targets":["127.0.0.1:9001"]}`
	gotRule := handlerData[model.GostRule](t, f.request(t, "PUT", rulePath, updateRule, 200))
	if gotRule.Type != model.RuleTypeTunnel || gotRule.TunnelID == nil || *gotRule.TunnelID != tunnel.ID || gotRule.ListenPort != 9101 {
		t.Fatalf("updated rule: %+v", gotRule)
	}
	f.request(t, "POST", "/rules", `{"name":"unassigned","type":"forward","protocol":"tcp","listen_port":9100}`, 400)
	if detail := handlerData[model.GostRule](t, f.request(t, "GET", rulePath, "", 200)); detail.Name != "Web UDP" {
		t.Fatalf("rule detail: %+v", detail)
	}
	rules := handlerData[handlerPage[model.GostRule]](t, f.request(t, "GET", fmt.Sprintf("/rules?tunnel_id=%d&type=tunnel&keyword=Web&status=stopped", tunnel.ID), "", 200))
	if rules.Total != 1 || len(rules.List) != 1 || rules.List[0].ID != rule.ID {
		t.Fatalf("filtered rule list: %+v", rules)
	}
	if reply := f.request(t, "POST", rulePath+"/start", "", 400); reply.Code != 10006 {
		t.Fatalf("starting a rule on an offline node: %+v", reply)
	}
	f.request(t, "POST", rulePath+"/stop", "", 200)
	f.request(t, "DELETE", tunnelPath, "", 400)
	stats := handlerData[service.DashboardStats](t, f.request(t, "GET", "/dashboard/stats", "", 200))
	if stats.Nodes.Offline != 2 || stats.Rules.TunnelType != 1 || stats.Tunnels.Total != 1 || stats.Database != "sqlite" {
		t.Fatalf("dashboard totals: %+v", stats)
	}
	f.request(t, "DELETE", rulePath, "", 200)
	f.request(t, "DELETE", tunnelPath, "", 200)
	f.request(t, "GET", rulePath, "", 404)
	f.request(t, "PUT", rulePath, updateRule, 404)
	f.request(t, "DELETE", rulePath, "", 404)
	f.request(t, "POST", rulePath+"/stop", "", 500)
	f.request(t, "GET", tunnelPath, "", 404)
	f.request(t, "PUT", tunnelPath, updateTunnel, 404)
	f.request(t, "DELETE", tunnelPath, "", 404)
	f.request(t, "POST", tunnelPath+"/stop", "", 500)
}

func TestHTTPRejectsMalformedParameters(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerForwarding()
	f.registerSettings()
	for _, resource := range []string{"/nodes", "/rules", "/tunnels"} {
		f.request(t, "GET", resource+"?page=-1", "", 400)
		f.request(t, "POST", resource, "{", 400)
		f.request(t, "GET", resource+"/invalid", "", 400)
		f.request(t, "PUT", resource+"/invalid", "{}", 400)
		f.request(t, "PUT", resource+"/1", "{", 400)
		f.request(t, "DELETE", resource+"/invalid", "", 400)
	}
	for _, resource := range []string{"/rules", "/tunnels"} {
		f.request(t, "POST", resource+"/invalid/start", "", 400)
		f.request(t, "POST", resource+"/invalid/stop", "", 400)
	}
	f.request(t, "GET", "/nodes/invalid/config", "", 400)
	f.request(t, "POST", "/nodes/0/registration-token", "", 400)
	f.request(t, "GET", "/logs?pageSize=101", "", 400)
}

func TestHTTPReportsUnavailableDatabase(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerNodes()
	f.registerForwarding()
	f.registerSettings()
	sqlDB, err := f.db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/nodes", "/rules", "/tunnels", "/logs", "/dashboard/stats", "/nodes/1/config",
		"/system/config", "/system/public-config", "/system/gost-allowlist", "/system/gost-ruleset",
	} {
		f.request(t, "GET", path, "", 500)
	}
	f.request(t, "PUT", "/system/config", clearSystemConfigPayload, 500)
}

func TestForwardingHTTPStartIsIdempotent(t *testing.T) {
	f := newHandlerHTTPFixture(t)
	f.registerForwarding()
	entry := model.GostNode{Name: "entry", Port: 39110, Status: model.NodeStatusOnline}
	exit := model.GostNode{Name: "exit", Port: 39111, Status: model.NodeStatusOnline}
	if err := f.db.Create(&entry).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.db.Create(&exit).Error; err != nil {
		t.Fatal(err)
	}
	// A repeated start must preserve the running objects even if their bridges cannot be reached.
	tunnel := model.GostTunnel{
		Name: "active tunnel", EntryNodeID: entry.ID, ExitNodeID: exit.ID,
		Protocol: "tcp", Status: model.TunnelStatusRunning, ChainID: "existing-chain",
	}
	if err := f.db.Create(&tunnel).Error; err != nil {
		t.Fatal(err)
	}
	rule := model.GostRule{
		Name: "active rule", Type: model.RuleTypeTunnel, TunnelID: &tunnel.ID,
		Protocol: model.RuleProtocolTCP, Status: model.RuleStatusRunning, ServiceID: "existing-service",
	}
	if err := f.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	tunnelPath, rulePath := fmt.Sprintf("/tunnels/%d", tunnel.ID), fmt.Sprintf("/rules/%d", rule.ID)
	f.request(t, "POST", tunnelPath+"/start", "", 200)
	f.request(t, "POST", rulePath+"/start", "", 200)
	if got := handlerData[model.GostTunnel](t, f.request(t, "GET", tunnelPath, "", 200)); got.Status != tunnel.Status || got.ChainID != tunnel.ChainID {
		t.Fatalf("repeated tunnel start changed configuration: %+v", got)
	}
	if got := handlerData[model.GostRule](t, f.request(t, "GET", rulePath, "", 200)); got.Status != rule.Status || got.ServiceID != rule.ServiceID {
		t.Fatalf("repeated rule start changed configuration: %+v", got)
	}
}
