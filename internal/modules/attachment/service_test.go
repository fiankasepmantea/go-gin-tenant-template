package attachment

import "testing"

func TestConfigValidate(t *testing.T) {
	good := Config{Name: "documents", Parent: "activities", Table: "activity_documents", IDColumn: "activity_document_id",
		ParentTable: "activities", ParentColumn: "activity_id"}
	if err := good.validate(); err != nil {
		t.Fatal(err)
	}
	if good.ParentPK != "activity_id" || good.Limit != 200 {
		t.Fatalf("defaults: %+v", good)
	}

	for name, mutate := range map[string]func(*Config){
		"sql in a table name": func(c *Config) { c.Table = "t; drop table users" },
		"uppercase":           func(c *Config) { c.Name = "Documents" },
		"empty":               func(c *Config) { c.IDColumn = "" },
		"dash":                func(c *Config) { c.Parent = "my-parent" },
		"dot":                 func(c *Config) { c.ParentTable = "public.activities" },
	} {
		c := good
		mutate(&c)
		if err := c.validate(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestNewServicePanicsOnABadConfig(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic")
		}
	}()
	NewService(Config{}, nil, nil)
}
