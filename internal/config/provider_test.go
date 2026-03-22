package config

import (
	"testing"
)

func TestConfigProvider_ScopeResolution(t *testing.T) {
	// Simulate a loaded ConfigProvider
	p := &ConfigProvider{
		values: map[string]map[int]map[string]string{
			ScopeDefault: {
				0: {
					"general/locale/timezone":  "America/New_York",
					"catalog/search/engine":    "opensearch",
					"web/secure/base_url":      "http://default.example.com/",
				},
			},
			ScopeWebsite: {
				1: {
					"web/secure/base_url": "http://website1.example.com/",
				},
			},
			ScopeStore: {
				2: {
					"web/secure/base_url": "http://store2.example.com/",
				},
			},
		},
		storeToWebsite: map[int]int{
			1: 1,
			2: 1,
		},
	}

	tests := []struct {
		name    string
		path    string
		storeID int
		want    string
	}{
		{"default when no store override", "general/locale/timezone", 1, "America/New_York"},
		{"default for store_id 0", "catalog/search/engine", 0, "opensearch"},
		{"store override", "web/secure/base_url", 2, "http://store2.example.com/"},
		{"website fallback when no store override", "web/secure/base_url", 1, "http://website1.example.com/"},
		{"default fallback when no store or website", "web/secure/base_url", 0, "http://default.example.com/"},
		{"missing path returns empty", "nonexistent/path", 1, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := p.Get(tt.path, tt.storeID)
			if got != tt.want {
				t.Errorf("Get(%q, %d) = %q, want %q", tt.path, tt.storeID, got, tt.want)
			}
		})
	}
}

func TestConfigProvider_TypedAccessors(t *testing.T) {
	p := &ConfigProvider{
		values: map[string]map[int]map[string]string{
			ScopeDefault: {
				0: {
					"customer/password/minimum_password_length": "8",
					"catalog/price/decimal":                    "2.50",
					"newsletter/general/active":                "1",
					"feature/disabled":                         "0",
					"bad/number":                               "not_a_number",
				},
			},
		},
		storeToWebsite: map[int]int{},
	}

	if v := p.GetInt("customer/password/minimum_password_length", 0, 6); v != 8 {
		t.Errorf("GetInt = %d, want 8", v)
	}
	if v := p.GetInt("nonexistent", 0, 42); v != 42 {
		t.Errorf("GetInt default = %d, want 42", v)
	}
	if v := p.GetInt("bad/number", 0, 99); v != 99 {
		t.Errorf("GetInt bad = %d, want 99", v)
	}

	if v := p.GetFloat("catalog/price/decimal", 0, 0); v != 2.50 {
		t.Errorf("GetFloat = %f, want 2.50", v)
	}

	if v := p.GetBool("newsletter/general/active", 0); !v {
		t.Error("GetBool should be true for '1'")
	}
	if v := p.GetBool("feature/disabled", 0); v {
		t.Error("GetBool should be false for '0'")
	}
	if v := p.GetBool("nonexistent", 0); v {
		t.Error("GetBool should be false for missing")
	}
}

func TestConfigProvider_GetWebsiteID(t *testing.T) {
	p := &ConfigProvider{
		storeToWebsite: map[int]int{1: 1, 2: 1, 3: 2},
	}
	if v := p.GetWebsiteID(3); v != 2 {
		t.Errorf("GetWebsiteID(3) = %d, want 2", v)
	}
	if v := p.GetWebsiteID(999); v != 1 {
		t.Errorf("GetWebsiteID(999) = %d, want 1 (default)", v)
	}
}
