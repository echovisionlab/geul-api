package model

import (
	"database/sql/driver"
	"time"

	"github.com/echovisionlab/geul-api/internal/structured"
)

// AddressComponents represents parsed address components from geocoding
type AddressComponents struct {
	Street     *string `json:"street,omitempty"`
	City       *string `json:"city,omitempty"`
	Region     *string `json:"region,omitempty"`
	Country    *string `json:"country,omitempty"`
	PostalCode *string `json:"postal_code,omitempty"`
}

// Scan implements sql.Scanner for AddressComponents (JSONB)
func (a *AddressComponents) Scan(value structured.Value) error {
	return ScanJSON(value, a)
}

// Value implements driver.Valuer for AddressComponents (JSONB)
func (a AddressComponents) Value() (driver.Value, error) {
	return ValueJSON(a)
}

// MapPlace represents a location on the map
// Maps to: sql/001_schema.sql - map_place table
type MapPlace struct {
	ID                string             `gorm:"column:id;type:uuid;primaryKey;default:gen_random_uuid()"`
	Name              string             `gorm:"column:name;type:varchar(255);not null"`
	Address           string             `gorm:"column:address;type:text;not null"`
	AddressComponents *AddressComponents `gorm:"column:address_components;type:jsonb"`
	Lat               float64            `gorm:"column:lat;type:numeric(10,8);not null"`
	Lng               float64            `gorm:"column:lng;type:numeric(11,8);not null"`
	GooglePlaceID     *string            `gorm:"column:google_place_id;type:varchar(255)"`
	ImageFileID       *string            `gorm:"column:image_file_id;type:uuid"`
	CreatedByMemberID *string            `gorm:"column:created_by_member_id;type:uuid"`
	UpdatedByMemberID *string            `gorm:"column:updated_by_member_id;type:uuid"`
	CreatedAt         time.Time          `gorm:"column:created_at;not null;default:now()"`
	UpdatedAt         time.Time          `gorm:"column:updated_at;not null;default:now()"`
}

// TableName returns the table name for GORM
func (MapPlace) TableName() string {
	return "map_place"
}
