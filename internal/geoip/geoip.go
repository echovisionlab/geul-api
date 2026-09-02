package geoip

import (
	"context"
	"database/sql"
	"net"
	"sync"
	"time"

	"gorm.io/gorm"
)

// Info holds GeoIP lookup result
type Info struct {
	CountryCode string
	CountryName string
	City        string
	Latitude    float64
	Longitude   float64
	IsProxy     bool
	IsSatellite bool
	TimeZone    string
}

type cacheEntry struct {
	info      *Info
	expiresAt time.Time
}

// Lookup provides GeoIP lookup with caching
type Lookup struct {
	db       *gorm.DB
	mu       sync.RWMutex
	cache    map[string]*cacheEntry
	cacheTTL time.Duration
}

const (
	defaultCacheTTL = 5 * time.Minute
	cleanupInterval = 10 * time.Minute
	maxCacheSize    = 10000
)

// NewLookup creates a new GeoIP lookup instance
func NewLookup(db *gorm.DB) *Lookup {
	l := &Lookup{
		db:       db,
		cache:    make(map[string]*cacheEntry),
		cacheTTL: defaultCacheTTL,
	}
	go l.cleanupLoop()
	return l
}

func (l *Lookup) cleanupLoop() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		now := time.Now()
		for k, v := range l.cache {
			if now.After(v.expiresAt) {
				delete(l.cache, k)
			}
		}
		l.mu.Unlock()
	}
}

func (l *Lookup) getCached(ip string) *Info {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if entry, ok := l.cache[ip]; ok && time.Now().Before(entry.expiresAt) {
		return entry.info
	}
	return nil
}

func (l *Lookup) setCache(ip string, info *Info) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Evict if cache is too large
	if len(l.cache) >= maxCacheSize {
		// Simple eviction: clear oldest 10%
		count := 0
		for k := range l.cache {
			delete(l.cache, k)
			count++
			if count >= maxCacheSize/10 {
				break
			}
		}
	}

	l.cache[ip] = &cacheEntry{
		info:      info,
		expiresAt: time.Now().Add(l.cacheTTL),
	}
}

// LookupIP looks up GeoIP info for the given IP address
func (l *Lookup) LookupIP(ctx context.Context, ipStr string) *Info {
	// Skip invalid or local IPs
	if ipStr == "" || ipStr == "anonymous" {
		return nil
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return nil
	}

	// Skip non-routable IPs (localhost, private, link-local, unspecified)
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return nil
	}

	// Check cache
	if cached := l.getCached(ipStr); cached != nil {
		return cached
	}

	// Query database
	info := l.queryDB(ctx, ipStr)

	// Cache result (even nil to avoid repeated lookups for unknown IPs)
	l.setCache(ipStr, info)

	return info
}

func (l *Lookup) queryDB(ctx context.Context, ip string) *Info {
	var result struct {
		CountryCode string          `gorm:"column:country_iso_code"`
		CountryName string          `gorm:"column:country_name"`
		City        string          `gorm:"column:city_name"`
		Latitude    sql.NullFloat64 `gorm:"column:lat"`
		Longitude   sql.NullFloat64 `gorm:"column:lng"`
		IsProxy     bool            `gorm:"column:is_anonymous_proxy"`
		IsSatellite bool            `gorm:"column:is_satellite_provider"`
		TimeZone    string          `gorm:"column:time_zone"`
	}

	err := l.db.WithContext(ctx).Raw(`
		SELECT
			l.country_iso_code,
			l.country_name,
			l.city_name,
			ST_Y(n.location::geometry) as lat,
			ST_X(n.location::geometry) as lng,
			n.is_anonymous_proxy,
			n.is_satellite_provider,
			l.time_zone
		FROM geoip_network n
		LEFT JOIN geoip_location l ON n.geoname_id = l.geoname_id
		WHERE n.network >>= ?::ipaddress
		LIMIT 1
	`, ip).Scan(&result).Error

	if err != nil || result.CountryCode == "" {
		return nil
	}

	return &Info{
		CountryCode: result.CountryCode,
		CountryName: result.CountryName,
		City:        result.City,
		Latitude:    result.Latitude.Float64,
		Longitude:   result.Longitude.Float64,
		IsProxy:     result.IsProxy,
		IsSatellite: result.IsSatellite,
		TimeZone:    result.TimeZone,
	}
}
