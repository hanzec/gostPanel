package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

// Version 编译时注入的版本号
var Version = "dev"

// Config 应用配置结构
type Config struct {
	Server    ServerConfig    `mapstructure:"server"`
	Database  DatabaseConfig  `mapstructure:"database"`
	Authentik AuthentikConfig `mapstructure:"authentik"`
	Report    ReportConfig    `mapstructure:"report"`
	NodeTLS   NodeTLSConfig   `mapstructure:"node_tls"`
	Log       LogConfig       `mapstructure:"log"`
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Port string `mapstructure:"port"`
	Mode string `mapstructure:"mode"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Type string `mapstructure:"type"`
	Path string `mapstructure:"path"`
	DSN  string `mapstructure:"dsn"`
}

type AuthentikConfig struct {
	TrustedProxies []string `mapstructure:"trusted_proxies"`
	AdminGroup     string   `mapstructure:"admin_group"`
}

type ReportConfig struct {
	Listen   string `mapstructure:"listen"`
	Address  string `mapstructure:"address"`
	CertFile string `mapstructure:"cert_file"`
	KeyFile  string `mapstructure:"key_file"`
}

type NodeTLSConfig struct {
	CAFile string `mapstructure:"ca_file"`
}

// LogConfig 日志配置
type LogConfig struct {
	Level  string `mapstructure:"level"`
	Format string `mapstructure:"format"`
	Output string `mapstructure:"output"`
}

// 全局配置实例
var cfg *Config

// Load 加载配置文件
// configPath: 配置文件路径，为空则使用默认路径
func Load(configPath string) (*Config, error) {
	v := viper.New()
	v.SetConfigType("yaml")
	if err := v.BindEnv("database.type", "YCGP_DATABASE_TYPE"); err != nil {
		return nil, err
	}
	if err := v.BindEnv("database.dsn", "YCGP_DATABASE_DSN"); err != nil {
		return nil, err
	}

	// 确定配置文件路径
	if configPath != "" {
		v.SetConfigFile(configPath)
	} else {
		// 获取可执行文件所在目录
		execPath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("获取可执行文件路径失败: %w", err)
		}
		execDir := filepath.Dir(execPath)

		// 添加配置文件搜索路径
		v.SetConfigName("config")
		v.AddConfigPath(filepath.Join(execDir, "config"))
		v.AddConfigPath("./config")
		v.AddConfigPath(".")
	}

	// 读取配置文件
	if err := v.ReadInConfig(); err != nil {
		var configFileNotFoundError viper.ConfigFileNotFoundError
		if !errors.As(err, &configFileNotFoundError) {
			return nil, fmt.Errorf("读取配置文件失败: %w", err)
		}
	}

	// 解析配置到结构体
	cfg = &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	// 设置默认值
	setDefaults(cfg)

	return cfg, nil
}

// setDefaults 设置配置默认值
func setDefaults(cfg *Config) {
	// 服务器默认配置
	if cfg.Server.Port == "" {
		cfg.Server.Port = ":39100"
	}
	if cfg.Server.Mode == "" {
		cfg.Server.Mode = "debug"
	}

	// 数据库默认配置
	if cfg.Database.Type == "" {
		cfg.Database.Type = "sqlite"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "./data/gost-panel.db"
	}

	if len(cfg.Authentik.TrustedProxies) == 0 {
		cfg.Authentik.TrustedProxies = []string{"127.0.0.1", "::1"}
	}
	if cfg.Authentik.AdminGroup == "" {
		cfg.Authentik.AdminGroup = "ycgp-admins"
	}

	// 日志默认配置
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "json"
	}
	if cfg.Log.Output == "" {
		cfg.Log.Output = "./logs/app.log"
	}
}

// Get 获取全局配置实例
func Get() *Config {
	if cfg == nil {
		panic("配置未初始化，请先调用 Load 方法")
	}
	return cfg
}
