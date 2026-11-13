package model

// ExitRelayHistory stores the Relay traffic collected during a previous assignment of an exit.
type ExitRelayHistory struct {
	ID          uint  `gorm:"primaryKey"`
	RuleID      uint  `gorm:"not null;index"`
	NodeID      uint  `gorm:"not null;index"`
	InputBytes  int64 `gorm:"not null"`
	OutputBytes int64 `gorm:"not null"`
}
