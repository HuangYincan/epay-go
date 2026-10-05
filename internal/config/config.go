// internal/config/config.go
package config

import (
	"fmt"
	"github.com/spf13/viper"
	"strings"
)

type Config struct {
	Server   ServerConfig   `mapstructure:"server"`
	Database DatabaseConfig `mapstructure:"database"`
	Redis    RedisConfig    `mapstructure:"redis"`
	JWT      JWTConfig      `mapstructure:"jwt"`
}

type ServerConfig struct {
	Port int    `mapstructure:"port"`
	Mode string `mapstructure:"mode"` // debug, release, test
}

type DatabaseConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	User     string `mapstructure:"user"`
	Password string `mapstructure:"password"`
	DBName   string `mapstructure:"dbname"`
	SSLMode  string `mapstructure:"sslmode"`
}

type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

type JWTConfig struct {
	Secret     string `mapstructure:"secret"`
	ExpireHour int    `mapstructure:"expire_hour"`
}

var Cfg *Config

func Load(path string) error {
	viper.SetConfigFile(path)
	viper.SetConfigType("yaml")

	// 环境变量覆盖
	viper.AutomaticEnv()

	// 绑定 Docker 环境变量
	viper.BindEnv("database.host", "DB_HOST")
	viper.BindEnv("database.port", "DB_PORT")
	viper.BindEnv("database.user", "DB_USER")
	viper.BindEnv("database.password", "DB_PASSWORD")
	viper.BindEnv("database.dbname", "DB_NAME")
	viper.BindEnv("database.sslmode", "DB_SSLMODE")
	viper.BindEnv("redis.host", "REDIS_HOST")
	viper.BindEnv("redis.port", "REDIS_PORT")
	viper.BindEnv("redis.password", "REDIS_PASSWORD")
	viper.BindEnv("jwt.secret", "JWT_SECRET")
	viper.BindEnv("server.mode", "GIN_MODE")

	if err := viper.ReadInConfig(); err != nil {
		return err
	}

	Cfg = &Config{}
	if err := viper.Unmarshal(Cfg); err != nil {
		return err
	}
	if err := ValidateJWT(Cfg.JWT); err != nil {
		return err
	}

	return nil
}

func ValidateJWT(cfg JWTConfig) error {
	known := []string{"your-secret-key", "your-super-secret-key-change-in-production", "your_jwt_secret_here_change_in_production"}
	if len(cfg.Secret) < 32 || strings.TrimSpace(cfg.Secret) != cfg.Secret {
		return fmt.Errorf("JWT_SECRET 必须设置为至少32字节的随机密钥")
	}
	for _, secret := range known {
		if cfg.Secret == secret {
			return fmt.Errorf("JWT_SECRET 不得使用示例值")
		}
	}
	if cfg.ExpireHour <= 0 {
		return fmt.Errorf("JWT有效期必须大于零")
	}
	return nil
}

func Get() *Config {
	return Cfg
}
