package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	// Ensure a clean environment so defaults apply.
	for _, k := range []string{"OM_ADDR", "OM_VOLUME_REF", "OM_INDEX_MIN", "OM_INDEX_MAX", "OM_RATE_PER_MIN"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.Addr != ":8080" {
		t.Errorf("Addr default = %q", c.Addr)
	}
	if c.VolumeRef != 20000 || c.IndexMin != 0.5 || c.IndexMax != 2.0 {
		t.Errorf("aggregation defaults = %+v", c)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("OM_ADDR", ":9999")
	t.Setenv("OM_VOLUME_REF", "5000")
	t.Setenv("OM_INDEX_MIN", "0.25")
	t.Setenv("OM_RATE_PER_MIN", "42")
	c := Load()
	if c.Addr != ":9999" || c.VolumeRef != 5000 || c.IndexMin != 0.25 || c.RatePerMin != 42 {
		t.Fatalf("overrides not applied: %+v", c)
	}
}

func TestLoadIgnoresGarbageNumbers(t *testing.T) {
	t.Setenv("OM_VOLUME_REF", "not-a-number")
	t.Setenv("OM_RATE_PER_MIN", "xyz")
	c := Load()
	if c.VolumeRef != 20000 || c.RatePerMin != 120 {
		t.Fatalf("garbage env should fall back to defaults: %+v", c)
	}
}
