package config

import (
	"flag"
	"fmt"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type AppConfig struct {
	HTTP         HTTPConfig         `mapstructure:"http"`
	Log          LogConfig          `mapstructure:"log"`
	Database     DBConfig           `mapstructure:"db"`
	Auth         AuthConfig         `mapstructure:"auth"`
	SeedUser     SeedUserConfig     `mapstructure:"seed_user"`
	Verification VerificationConfig `mapstructure:"verification"`
	SMTP         SMTPConfig         `mapstructure:"smtp"`
	Cleanup      CleanupConfig      `mapstructure:"cleanup"`
	Storage      StorageConfig      `mapstructure:"storage"`
	Images       ImagesConfig       `mapstructure:"images"`
}

type HTTPConfig struct {
	Address           string        `mapstructure:"address"`
	ReadHeaderTimeout time.Duration `mapstructure:"read_header_timeout"`
	// ReadTimeout covers the whole request body, including image uploads.
	ReadTimeout time.Duration `mapstructure:"read_timeout"`
	// CORSAllowedOrigins lists browser origins (e.g. the web app) allowed to
	// call the API. One "*" wildcard per origin is supported, as in "http://localhost:*".
	CORSAllowedOrigins []string      `mapstructure:"cors_allowed_origins"`
	WriteTimeout       time.Duration `mapstructure:"write_timeout"`
	IdleTimeout        time.Duration `mapstructure:"idle_timeout"`
}

type LogConfig struct {
	Level string `mapstructure:"level"`
}

type DBConfig struct {
	URL string `mapstructure:"url"`

	MaxOpenConns    int           `mapstructure:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time"`
}

type AuthConfig struct {
	AccessSecret  string        `mapstructure:"access_secret"`
	RefreshSecret string        `mapstructure:"refresh_secret"`
	AccessTTL     time.Duration `mapstructure:"access_ttl"`
	RefreshTTL    time.Duration `mapstructure:"refresh_ttl"`
	Issuer        string        `mapstructure:"issuer"`
	Audience      string        `mapstructure:"audience"`
}

type SeedUserConfig struct {
	Email    string `mapstructure:"email"`
	Password string `mapstructure:"password"`
	FullName string `mapstructure:"full_name"`
}

type VerificationConfig struct {
	Secret         string        `mapstructure:"secret"`
	TTL            time.Duration `mapstructure:"ttl"`
	ResendInterval time.Duration `mapstructure:"resend_interval"`
	MaxAttempts    int           `mapstructure:"max_attempts"`
	PublicBaseURL  string        `mapstructure:"public_base_url"`
}

type SMTPConfig struct {
	Enabled   bool          `mapstructure:"enabled"`
	Host      string        `mapstructure:"host"`
	Port      int           `mapstructure:"port"`
	TLSMode   string        `mapstructure:"tls_mode"`
	Username  string        `mapstructure:"username"`
	Password  string        `mapstructure:"password"`
	FromEmail string        `mapstructure:"from_email"`
	FromName  string        `mapstructure:"from_name"`
	Timeout   time.Duration `mapstructure:"timeout"`
}

// StorageConfig points to S3-compatible object storage for listing images.
type StorageConfig struct {
	Endpoint  string `mapstructure:"endpoint"`
	Region    string `mapstructure:"region"`
	Bucket    string `mapstructure:"bucket"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	UseSSL    bool   `mapstructure:"use_ssl"`
}

type ImagesConfig struct {
	MaxSizeBytes  int64 `mapstructure:"max_size_bytes"`
	MaxPerListing int   `mapstructure:"max_per_listing"`
	// MaxDimension limits both width and height in pixels.
	MaxDimension int `mapstructure:"max_dimension"`
}

// CleanupConfig controls deletion of expired refresh sessions and email verifications.
type CleanupConfig struct {
	Interval  time.Duration `mapstructure:"interval"`
	Retention time.Duration `mapstructure:"retention"`
}

func Load() (*AppConfig, error) {

	var configPath, envPath string

	flag.StringVar(&configPath, "config", "", "")
	flag.StringVar(&envPath, "env", "", "")

	flag.Parse()

	if configPath == "" {
		return nil, fmt.Errorf("config path is empty")
	}

	if envPath != "" {
		if err := godotenv.Load(envPath); err != nil {
			return nil, fmt.Errorf("load env file %q: %w", envPath, err)
		}
	}

	v := viper.New()
	v.SetConfigFile(configPath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("read config file %q: %w", configPath, err)
	}

	bindings := map[string]string{
		"http.address":                 "HTTP_ADDRESS",
		"http.cors_allowed_origins":    "CORS_ALLOWED_ORIGINS",
		"log.level":                    "LOG_LEVEL",
		"db.url":                       "DATABASE_URL",
		"auth.access_secret":           "JWT_ACCESS_SECRET",
		"auth.refresh_secret":          "JWT_REFRESH_SECRET",
		"auth.access_ttl":              "JWT_ACCESS_TTL",
		"auth.refresh_ttl":             "JWT_REFRESH_TTL",
		"auth.issuer":                  "JWT_ISSUER",
		"auth.audience":                "JWT_AUDIENCE",
		"seed_user.email":              "SEED_USER_EMAIL",
		"seed_user.password":           "SEED_USER_PASSWORD",
		"seed_user.full_name":          "SEED_USER_FULL_NAME",
		"verification.secret":          "EMAIL_VERIFICATION_SECRET",
		"verification.ttl":             "EMAIL_VERIFICATION_TTL",
		"verification.resend_interval": "EMAIL_VERIFICATION_RESEND_INTERVAL",
		"verification.max_attempts":    "EMAIL_VERIFICATION_MAX_ATTEMPTS",
		"verification.public_base_url": "PUBLIC_BASE_URL",
		"smtp.enabled":                 "SMTP_ENABLED",
		"smtp.host":                    "SMTP_HOST",
		"smtp.port":                    "SMTP_PORT",
		"smtp.tls_mode":                "SMTP_TLS_MODE",
		"smtp.username":                "SMTP_USERNAME",
		"smtp.password":                "SMTP_PASSWORD",
		"smtp.from_email":              "SMTP_FROM_EMAIL",
		"smtp.from_name":               "SMTP_FROM_NAME",
		"smtp.timeout":                 "SMTP_TIMEOUT",
		"cleanup.interval":             "CLEANUP_INTERVAL",
		"cleanup.retention":            "CLEANUP_RETENTION",
		"storage.endpoint":             "S3_ENDPOINT",
		"storage.region":               "S3_REGION",
		"storage.bucket":               "S3_BUCKET",
		"storage.access_key":           "S3_ACCESS_KEY",
		"storage.secret_key":           "S3_SECRET_KEY",
		"storage.use_ssl":              "S3_USE_SSL",
	}
	for key, value := range bindings {
		if err := v.BindEnv(key, value); err != nil {
			return nil, fmt.Errorf("bind %s: %w", value, err)
		}
	}

	var config AppConfig
	if err := v.UnmarshalExact(&config); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}

	if config.Cleanup.Interval < time.Minute || config.Cleanup.Retention < 0 {
		return nil, fmt.Errorf("cleanup.interval must be >= 1m and cleanup.retention must be >= 0")
	}

	return &config, nil
}

func MustLoad() *AppConfig {
	config, err := Load()
	if err != nil {
		panic(fmt.Errorf("couldn't load config: %w", err))
	}
	return config
}
