package service

import (
	"net/http"
	"testing"

	"yagp/internal/dto"
	bizerrors "yagp/internal/errors"
	"yagp/internal/model"
)

func TestRuleRemovalRetainsUnconfirmedRuntime(t *testing.T) {
	for _, state := range []model.RuleStatus{model.RuleStatusRunning, model.RuleStatusError, model.RuleStatusStopped} {
		for _, operation := range []string{"stop", "delete"} {
			t.Run(string(state)+"/"+operation, func(t *testing.T) {
				db := newServiceTestDB(t)
				loadServiceConfig(t, "report:\n  listen: 127.0.0.1:39001\n")
				remote := &lifecycleNode{t: t}
				node := startServiceTestNode(t, remote)
				saveServiceFixtures(t, db, &node)
				service := NewRuleService(db)
				rule, err := service.Create(&dto.CreateRuleReq{
					NodeID: &node.ID, Name: "tracked listener", Protocol: "tcp", ListenPort: 8080, Targets: []string{"target.test:443"},
				}, testAdmin, testIP, testAgent)
				requireServiceError(t, err, nil)
				requireServiceError(t, service.Start(rule.ID, testAdmin, testIP, testAgent), nil)
				rule, err = service.GetByID(rule.ID)
				requireServiceError(t, err, nil)
				serviceID := rule.ServiceID
				updateServiceFields(t, db, rule, map[string]any{
					"status": state, "input_bytes": 12, "output_bytes": 8, "total_bytes": 20, "observer_input_bytes": 12,
				})
				remove := service.Stop
				if operation == "delete" {
					remove = service.Delete
				}
				remote.rejectCommand("DELETE /config/services/"+serviceID, http.StatusServiceUnavailable)
				if err := remove(rule.ID, testAdmin, testIP, testAgent); err == nil {
					t.Fatal("failed remote deletion was reported as successful")
				}
				stored, err := service.GetByID(rule.ID)
				requireServiceError(t, err, nil)
				if stored.Status != state || stored.ServiceID != serviceID || remote.service(serviceID).Name == "" {
					t.Fatalf("failed removal changed tracked runtime: %+v", stored)
				}
				var audits int64
				requireServiceError(t, db.Model(&model.OperationLog{}).Where("action IN ?", []string{model.ActionStop, model.ActionDelete}).Count(&audits).Error, nil)
				if audits != 0 {
					t.Fatal("failed removal emitted a success audit")
				}
				remote.rejectCommand("DELETE /config/services/"+serviceID, 0)
				requireServiceError(t, remove(rule.ID, testAdmin, testIP, testAgent), nil)
				if remote.service(serviceID).Name != "" {
					t.Fatal("successful removal left the listener running")
				}
				stored, err = service.GetByID(rule.ID)
				if operation == "delete" {
					requireServiceError(t, err, bizerrors.ErrRuleNotFound)
					return
				}
				requireServiceError(t, err, nil)
				if stored.Status != model.RuleStatusStopped || stored.ServiceID != "" || stored.TotalBytes != 20 || stored.ObserverCounters.InputBytes != 12 {
					t.Fatalf("confirmed stop did not preserve counters and clear runtime: %+v", stored)
				}
				requireServiceError(t, service.Start(rule.ID, testAdmin, testIP, testAgent), nil)
				stored, err = service.GetByID(rule.ID)
				requireServiceError(t, err, nil)
				if stored.ServiceID != serviceID || stored.Status != model.RuleStatusRunning || remote.service(serviceID).Name == "" || stored.TotalBytes != 20 {
					t.Fatalf("restart failed to recreate the same listener: %+v", stored)
				}
			})
		}
	}
}

func TestRuleRemovalRetriesConfigSaveBeforeForgettingService(t *testing.T) {
	for _, operation := range []string{"stop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			db := newServiceTestDB(t)
			remote := &lifecycleNode{t: t}
			node := startServiceTestNode(t, remote)
			saveServiceFixtures(t, db, &node)
			rule := model.GostRule{Name: "already absent listener", NodeID: &node.ID, ServiceID: "rule-1", Status: model.RuleStatusRunning, ListenPort: 8080}
			saveServiceFixtures(t, db, &rule)
			service := NewRuleService(db)
			remove := service.Stop
			if operation == "delete" {
				remove = service.Delete
			}
			remote.rejectCommand("POST /config?format=yaml", http.StatusServiceUnavailable)
			if err := remove(rule.ID, testAdmin, testIP, testAgent); err == nil {
				t.Fatal("failed config save was reported as successful")
			}
			stored, err := service.GetByID(rule.ID)
			requireServiceError(t, err, nil)
			if stored.ServiceID != rule.ServiceID || stored.Status != rule.Status {
				t.Fatalf("unpersisted deletion lost its retry identity: %+v", stored)
			}
			remote.rejectCommand("POST /config?format=yaml", 0)
			requireServiceError(t, remove(rule.ID, testAdmin, testIP, testAgent), nil)
			if operation == "stop" {
				updateServiceFields(t, db, &node, map[string]any{"status": model.NodeStatusOffline})
				requireServiceError(t, service.Delete(rule.ID, testAdmin, testIP, testAgent), nil)
			}
			_, err = service.GetByID(rule.ID)
			requireServiceError(t, err, bizerrors.ErrRuleNotFound)
		})
	}
}

func TestRuleRemovalRequiresNodeOnlyForTrackedServices(t *testing.T) {
	for _, operation := range []string{"stop", "delete"} {
		t.Run(operation, func(t *testing.T) {
			db := newServiceTestDB(t)
			node := model.GostNode{Name: "offline", Status: model.NodeStatusOffline}
			saveServiceFixtures(t, db, &node)
			rule := model.GostRule{Name: "tracked", NodeID: &node.ID, ServiceID: "rule-1", Status: model.RuleStatusError, ListenPort: 8080}
			saveServiceFixtures(t, db, &rule)
			service := NewRuleService(db)
			remove := service.Stop
			if operation == "delete" {
				remove = service.Delete
			}
			requireServiceError(t, remove(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrNodeOffline)
			requireServiceError(t, db.Delete(&node).Error, nil)
			requireServiceError(t, remove(rule.ID, testAdmin, testIP, testAgent), bizerrors.ErrNodeNotFound)
			stored, err := service.GetByID(rule.ID)
			requireServiceError(t, err, nil)
			if stored.ServiceID != rule.ServiceID || stored.Status != rule.Status {
				t.Fatal("unavailable node lost its tracked runtime")
			}
			updateServiceFields(t, db, &rule, map[string]any{"service_id": "", "status": model.RuleStatusStopped})
			requireServiceError(t, remove(rule.ID, testAdmin, testIP, testAgent), nil)
		})
	}
}
