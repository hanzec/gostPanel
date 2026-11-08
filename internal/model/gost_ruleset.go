package model

import "time"

// GostRuleset is the panel's desired destination policy and its last successful conversion.
type GostRuleset struct {
	ID        uint       `gorm:"primaryKey" json:"-"`
	URL       string     `gorm:"type:text" json:"url"`
	NodeIDs   []uint     `gorm:"serializer:json;type:text" json:"node_ids"`
	Matchers  []string   `gorm:"serializer:json;type:longtext" json:"matchers"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}
