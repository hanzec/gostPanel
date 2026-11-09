package model

// NodeCredentials stores the public node credentials and the latest refresh receipt.
type NodeCredentials struct {
	SessionGeneration        uint64 `gorm:"not null;default:0"`
	AccessTokenHash          string `gorm:"size:64;not null;default:''"`
	AccessExpiresAt          int64  `gorm:"not null;default:0"`
	RefreshTokenHash         string `gorm:"size:64;not null;default:''"`
	RefreshExpiresAt         int64  `gorm:"not null;default:0"`
	PreviousRefreshTokenHash string `gorm:"size:64;not null;default:''"`
	RefreshRequestHash       string `gorm:"size:64;not null;default:''"`
	RefreshReceipt           []byte
	RefreshRetryUntil        int64 `gorm:"not null;default:0"`
}
