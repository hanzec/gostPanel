package model

// GostAllowlist stores the panel's desired source addresses for tunnel relays.
type GostAllowlist struct {
	ID        uint     `gorm:"primaryKey" json:"-"`
	NodeIDs   []uint   `gorm:"type:text;serializer:json" json:"node_ids"`
	Addresses []string `gorm:"type:text;serializer:json" json:"addresses"`
}
