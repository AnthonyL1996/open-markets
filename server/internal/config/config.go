// Package config loads the server's runtime configuration from environment variables, with sane
// defaults so the service runs out of the box (./openmarketsd needs no env to start).
package config

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Config is the fully-resolved runtime configuration. Construct via Load.
type Config struct {
	Addr                string        // listen address, e.g. ":8080"
	DataPath            string        // JSON snapshot file for the in-memory store
	DBURL               string        // Postgres connection URL; when set, the Postgres store replaces the JSON store
	Version             string        // reported by /health and embedded in feed responses
	VolumeRef           float64       // net-supply units that map to a full index swing (mirrors the client's VolumeRef)
	IndexMin            float64       // hard floor for any published index (matches the client's MinIndex)
	IndexMax            float64       // hard ceiling (matches the client's MaxIndex)
	ReadTimeout         time.Duration // per-request read timeout
	WriteTimeout        time.Duration // per-request write timeout
	RatePerMin          int           // max requests per minute per client (0 disables limiting)
	AcctPerHour         int           // max new accounts per hour per client IP on POST /accounts (0 disables)
	Console             bool          // serve the /console operator UI (default on; disable in production)
	ConsoleToken        string        // admin token gating /console; empty = unauthenticated (logged as a warning)
	DueInterval         time.Duration // real time that equals one installment period (due-clock); default fast-dev 2m
	DueGrace            int           // extra intervals before a due installment is missed (default 1)
	DueMaxMissesPerTick int           // cap on overdue installments processed per item per tick (backlog drain)
	DueOfflineGrace     int           // extra grace intervals for an obligor that appears offline (0 = off)
	DueOfflineThreshold time.Duration // time since last activity after which an obligor counts as offline
	TrustProxy          bool          // honor CF-Connecting-IP / X-Forwarded-For for the client IP (only behind a trusted reverse proxy)
	DiscordWebhook      string        // Discord webhook URL; when set, a background poster bridges new league activity to Discord
	DiscordInterval     time.Duration // how often the Discord poster polls for new settlement events (default 30s)
	ChronicleInterval   time.Duration // how often the chronicler scans leagues for austerity/record-trade transitions (default 60s)
	CrisisInterval      time.Duration // how often the crisis scheduler advances/maybe-starts a shared league crisis (default 120s, min 10s)
	CrisisChance        float64       // per-interval probability of STARTING a new crisis (default 0.15)
	ProjectInterval     time.Duration // how often the project generator ensures each league has an open Great Work (default 300s, min 30s)
}

// Load reads OM_* environment variables, falling back to defaults that match the C# client's
// expectations (MinIndex 0.5 / MaxIndex 2.0 / VolumeRef 20000).
func Load() Config {
	cfg := Config{
		Addr:                env("OM_ADDR", ":8080"),
		DataPath:            env("OM_DATA", "data/openmarkets.json"),
		DBURL:               env("OM_DB_URL", ""), // empty → in-memory/JSON store; set → Postgres backend
		Version:             env("OM_VERSION", "phase-a"),
		VolumeRef:           envFloat("OM_VOLUME_REF", 20000),
		IndexMin:            envFloat("OM_INDEX_MIN", 0.5),
		IndexMax:            envFloat("OM_INDEX_MAX", 2.0),
		ReadTimeout:         time.Duration(envInt("OM_READ_TIMEOUT_SEC", 10)) * time.Second,
		WriteTimeout:        time.Duration(envInt("OM_WRITE_TIMEOUT_SEC", 10)) * time.Second,
		RatePerMin:          envInt("OM_RATE_PER_MIN", 120),
		AcctPerHour:         envInt("OM_ACCT_PER_HOUR", 10), // stricter, dedicated limit on UNAUTHENTICATED account creation; 0 = unlimited (local testing)
		Console:             envInt("OM_CONSOLE", 1) != 0,
		ConsoleToken:        env("OM_CONSOLE_TOKEN", ""),                                      // admin token for /console; empty keeps the local-dev console open (warned at startup)
		DueInterval:         time.Duration(envInt("OM_DUE_INTERVAL_SEC", 2700)) * time.Second, // ≈ one in-game day at 1× (45 min), so the wall-clock period matches the client's day rhythm; lower it for fast testing
		DueGrace:            envInt("OM_DUE_GRACE", 1),
		DueMaxMissesPerTick: envInt("OM_DUE_MAX_MISSES_PER_TICK", 4),
		DueOfflineGrace:     envInt("OM_DUE_OFFLINE_GRACE", 5), // away players get 5 extra periods before auto-bond
		DueOfflineThreshold: time.Duration(envInt("OM_DUE_OFFLINE_THRESHOLD_SEC", 120)) * time.Second,
		TrustProxy:          envInt("OM_TRUST_PROXY", 0) != 0, // off by default; enable ONLY behind a trusted reverse proxy / tunnel
		DiscordWebhook:      env("OM_DISCORD_WEBHOOK", ""),    // empty → the Discord activity bridge stays off
		DiscordInterval:     time.Duration(envInt("OM_DISCORD_INTERVAL_SEC", 30)) * time.Second,
		ChronicleInterval:   time.Duration(envInt("OM_CHRONICLE_INTERVAL_SEC", 60)) * time.Second, // always-on saga narrator
		CrisisInterval:      time.Duration(envInt("OM_CRISIS_INTERVAL_SEC", 120)) * time.Second,   // shared league crisis scheduler
		CrisisChance:        envFloat("OM_CRISIS_CHANCE", 0.15),                                    // per-interval chance to start a crisis
		ProjectInterval:     time.Duration(envInt("OM_PROJECT_INTERVAL_SEC", 300)) * time.Second,   // co-op megaproject (Great Works) generator
	}
	cfg.validate()
	return cfg
}

// validate clamps out-of-range numeric config to safe defaults (logging a warning rather than serving nonsense),
// so a typo'd env var can't, e.g., invert the index bounds or make VolumeRef zero (a divide-by-zero in pricing).
func (c *Config) validate() {
	if c.VolumeRef <= 0 {
		log.Printf("[config] OM_VOLUME_REF=%v is not > 0; clamping to 20000", c.VolumeRef)
		c.VolumeRef = 20000
	}
	if c.IndexMin >= c.IndexMax {
		log.Printf("[config] OM_INDEX_MIN (%v) >= OM_INDEX_MAX (%v); resetting to 0.5/2.0", c.IndexMin, c.IndexMax)
		c.IndexMin, c.IndexMax = 0.5, 2.0
	}
	if c.DueInterval <= 0 {
		log.Printf("[config] OM_DUE_INTERVAL_SEC <= 0 (%v); clamping to 2700s", c.DueInterval)
		c.DueInterval = 2700 * time.Second
	}
	if c.RatePerMin < 0 {
		log.Printf("[config] OM_RATE_PER_MIN=%d is negative; clamping to 0 (disabled)", c.RatePerMin)
		c.RatePerMin = 0
	}
	if c.AcctPerHour < 0 {
		log.Printf("[config] OM_ACCT_PER_HOUR=%d is negative; clamping to 0 (disabled)", c.AcctPerHour)
		c.AcctPerHour = 0
	}
	if c.DiscordInterval <= 0 {
		log.Printf("[config] OM_DISCORD_INTERVAL_SEC <= 0 (%v); clamping to 30s", c.DiscordInterval)
		c.DiscordInterval = 30 * time.Second
	}
	if c.ChronicleInterval < 5*time.Second {
		log.Printf("[config] OM_CHRONICLE_INTERVAL_SEC too low (%v); clamping to 5s", c.ChronicleInterval)
		c.ChronicleInterval = 5 * time.Second
	}
	if c.CrisisInterval < 10*time.Second {
		log.Printf("[config] OM_CRISIS_INTERVAL_SEC too low (%v); clamping to 10s", c.CrisisInterval)
		c.CrisisInterval = 10 * time.Second
	}
	if c.CrisisChance < 0 || c.CrisisChance > 1 {
		log.Printf("[config] OM_CRISIS_CHANCE out of [0,1] (%v); resetting to 0.15", c.CrisisChance)
		c.CrisisChance = 0.15
	}
	if c.ProjectInterval < 30*time.Second {
		log.Printf("[config] OM_PROJECT_INTERVAL_SEC too low (%v); clamping to 30s", c.ProjectInterval)
		c.ProjectInterval = 30 * time.Second
	}
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
		log.Printf("[config] %s=%q is not a valid integer; using default %d", key, v, def)
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
		log.Printf("[config] %s=%q is not a valid number; using default %v", key, v, def)
	}
	return def
}
