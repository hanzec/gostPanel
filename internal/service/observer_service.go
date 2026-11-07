package service

import (
	"context"
	"errors"

	"ycgp/internal/model"
	"ycgp/internal/repository"

	"gorm.io/gorm"
)

var ErrReportNodeMismatch = errors.New("rule does not belong to the reporting node")

type TrafficSample struct {
	RuleID uint
	model.ObserverCounters
	Reset bool
}

type ObserverService struct {
	db *gorm.DB
}

func NewObserverService(db *gorm.DB) *ObserverService {
	return &ObserverService{db: db}
}

func (s *ObserverService) RecordTraffic(ctx context.Context, nodeID uint, samples []TrafficSample) error {
	if len(samples) == 0 {
		return nil
	}

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		rules := repository.NewRuleRepository(tx)
		nodes := repository.NewNodeRepository(tx)
		for _, sample := range samples {
			var rule model.GostRule
			err := tx.Preload("Tunnel").First(&rule, sample.RuleID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue // The service can still report its last interval after deletion.
			}
			if err != nil {
				return err
			}
			if !reportsForNode(&rule, nodeID) {
				return ErrReportNodeMismatch
			}
			var observed, delta model.ObserverCounters
			if !sample.Reset {
				observed = sample.ObserverCounters
				delta = model.ObserverCounters{
					InputBytes:  counterIncrease(observed.InputBytes, rule.ObserverCounters.InputBytes),
					OutputBytes: counterIncrease(observed.OutputBytes, rule.ObserverCounters.OutputBytes),
					Connections: counterIncrease(observed.Connections, rule.ObserverCounters.Connections),
				}
			}
			if err := rules.UpdateTrafficSample(rule.ID, delta, observed); err != nil {
				return err
			}
			if delta.InputBytes != 0 || delta.OutputBytes != 0 {
				if err := nodes.UpdateStats(nodeID, delta.InputBytes, delta.OutputBytes); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func counterIncrease(observed, previous int64) int64 {
	if observed < previous {
		return observed
	}
	return observed - previous
}

func reportsForNode(rule *model.GostRule, nodeID uint) bool {
	if rule.Type == model.RuleTypeTunnel {
		return rule.Tunnel != nil && rule.Tunnel.EntryNodeID == nodeID
	}
	return rule.NodeID != nil && *rule.NodeID == nodeID
}
