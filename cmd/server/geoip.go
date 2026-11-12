package main

import (
	"context"

	"yagp/pkg/geoip"
	"yagp/pkg/logger"
)

func startGeoIP(ctx context.Context, path, downloadURL string) *geoip.Database {
	db := geoip.NewDatabase(path, downloadURL)
	if err := db.Load(); err != nil {
		logger.Warnf("GeoIP 本地数据库不可用，暂时使用在线查询: %s: %v", path, err)
	} else {
		logger.Infof("GeoIP 本地数据库已加载: %s", path)
	}

	logger.Infof("GeoIP 数据库后台下载开始: %s", path)
	go func() {
		if err := db.Refresh(ctx); err != nil {
			if ctx.Err() != nil {
				logger.Infof("GeoIP 数据库后台下载已取消: %s", path)
			} else if db.Ready() {
				logger.Warnf("GeoIP 数据库下载更新失败，继续使用本地缓存: %s: %v", path, err)
			} else {
				logger.Warnf("GeoIP 数据库下载更新失败，使用在线查询: %s: %v", path, err)
			}
			return
		}
		logger.Infof("GeoIP 数据库下载更新成功: %s", path)
	}()
	return db
}
