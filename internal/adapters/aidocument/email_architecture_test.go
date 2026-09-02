package aidocumentadapter

import (
	"bytes"
	"os"
	"testing"
)

func TestEmailProfileAdaptersContainNoPersistenceOrSQL(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"email_richtext.go", "email_template.go", "email_layout.go", "campaign.go"} {
		source, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range [][]byte{
			[]byte("gorm.io/"),
			[]byte("database/sql"),
			[]byte(".Exec("),
			[]byte(".Raw("),
			[]byte("Table("),
		} {
			if bytes.Contains(source, forbidden) {
				t.Fatalf("%s contains persistence boundary %q", name, forbidden)
			}
		}
	}
}
