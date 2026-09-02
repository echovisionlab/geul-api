package model

import "time"

// Genre represents a music genre
// Maps to: schema migrations - genre table
type Genre struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Name        string    `gorm:"column:name"`
	Slug        string    `gorm:"column:slug"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (Genre) TableName() string {
	return "genre"
}

// Style represents a music style (sub-genre)
// Maps to: schema migrations - style table
type Style struct {
	ID          string    `gorm:"column:id;primaryKey"`
	Name        string    `gorm:"column:name"`
	Slug        string    `gorm:"column:slug"`
	Description *string   `gorm:"column:description"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (Style) TableName() string {
	return "style"
}

// Format represents a release format (CD, Vinyl, etc.)
// Maps to: schema migrations - format table
// Note: Format has no description or created_at
type Format struct {
	ID   string `gorm:"column:id;primaryKey"`
	Name string `gorm:"column:name"`
	Slug string `gorm:"column:slug"`
}

func (Format) TableName() string {
	return "format"
}
