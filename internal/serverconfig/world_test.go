package serverconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTimeOfDay(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "world-layers.json"), []byte(`{"rows":[
		{"key":"bam_high_tod_1_morning","label":"Morning","category":"Lighting","map":"bam"},
		{"key":"bam_high_tod_5_night","label":"Night","category":"Lighting","map":"bam"},
		{"key":"grom_tod_5_night","label":"Night","category":"Lighting","map":"grom"},
		{"key":"bam_xmas","label":"Christmas","category":"Seasonal","map":"bam"}]}`), 0o644)
	all, err := ReadWorldLayers(dir)
	if err != nil || len(all) != 4 {
		t.Fatalf("%d layers, %v", len(all), err)
	}

	layers := map[string]string{"bam_xmas": "on"}
	if got := TimeOfDay(layers, all); got != "default" {
		t.Errorf("fresh: %q", got)
	}
	if err := ApplyTimeOfDay(layers, all, "night"); err != nil {
		t.Fatal(err)
	}
	if layers["bam_high_tod_5_night"] != "on" || layers["grom_tod_5_night"] != "on" || layers["bam_high_tod_1_morning"] != "off" || layers["bam_xmas"] != "on" {
		t.Errorf("night: %v", layers)
	}
	if got := TimeOfDay(layers, all); got != "night" {
		t.Errorf("after night: %q", got)
	}
	layers["grom_tod_5_night"] = "off"
	if got := TimeOfDay(layers, all); got != "custom" {
		t.Errorf("one layer changed by hand: %q", got)
	}
	ApplyTimeOfDay(layers, all, "default")
	if len(layers) != 1 || TimeOfDay(layers, all) != "default" {
		t.Errorf("back to default: %v", layers)
	}

	if none, err := ReadWorldLayers(t.TempDir()); none != nil || err != nil {
		t.Errorf("no file: %v %v", none, err)
	}
	if err := ApplyTimeOfDay(map[string]string{}, all[3:], "noon"); err == nil {
		t.Error("no time layers should be an error, as the server says")
	}
}

func TestWorldLayersOncePerKey(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "world-layers.json"), []byte(`{"rows":[
		{"key":"bam_xmas","label":"Christmas","map":"bam"},
		{"key":"","label":"No key","map":"bam"},
		{"key":"bam_xmas","label":"Christmas again","map":"bam"}]}`), 0o644)
	all, err := ReadWorldLayers(dir)
	if err != nil || len(all) != 1 || all[0].Label != "Christmas" {
		t.Errorf("%+v %v", all, err)
	}
}
