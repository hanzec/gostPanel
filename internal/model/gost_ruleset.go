package model

import "time"

// GostRuleset stores a subscription and its last successful conversion.
type GostRuleset struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	Name      string     `gorm:"size:100" json:"name"`
	URL       string     `gorm:"type:text" json:"url"`
	NodeIDs   []uint     `gorm:"serializer:json;type:text" json:"node_ids"`
	Matchers  []string   `gorm:"serializer:json;type:longtext" json:"matchers"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
