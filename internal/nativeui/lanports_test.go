package nativeui

// The LAN party tab's name and three port fields, as pickerConfig reads them.

import (
	"strings"
	"testing"

	"jetris/internal/config"
	"jetris/internal/prefs"
)

func TestPickerLANPorts(t *testing.T) {
	a := NewWithPicker(config.Config{}, nil, "", prefs.DefaultFavorites())
	a.th = newTestApp().th
	a.connTab = connTabLAN

	// Fresh: the three defaults.
	cfg, err := a.pickerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.RunEmbedded || cfg.EmbeddedPort != config.DefaultEmbeddedPort || cfg.EmbeddedWSPort != config.DefaultEmbeddedWSPort || cfg.EmbeddedHTTPPort != config.DefaultEmbeddedHTTPPort {
		t.Fatalf("pickerConfig fresh = %+v, want LAN mode on the default ports", cfg)
	}
	// The Name field starts on the default and reads back as it: the config
	// carries the field, the label the default, so the lobby bar reads
	// "<you> @ Jetris LAN Party nats server".
	if a.connNameEd.Text() != config.DefaultEmbeddedName || cfg.EmbeddedName != config.DefaultEmbeddedName {
		t.Fatalf("fresh name field %q / config %q, want %q", a.connNameEd.Text(), cfg.EmbeddedName, config.DefaultEmbeddedName)
	}
	if name, _ := connectionParts(cfg, "nats://192.168.1.23:4222", ""); name != config.DefaultEmbeddedName {
		t.Fatalf("connection label = %q, want the default name", name)
	}
	// A name of the host's own heads the label; blank falls back to the
	// default; a paragraph is refused.
	a.connNameEd.SetText("  Basement party  ")
	cfg, err = a.pickerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := connectionParts(cfg, "nats://192.168.1.23:4222", ""); cfg.EmbeddedName != "Basement party" || name != "Basement party" {
		t.Fatalf("named party: config %q label %q, want Basement party", cfg.EmbeddedName, name)
	}
	a.connNameEd.SetText("")
	cfg, err = a.pickerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := connectionParts(cfg, "nats://192.168.1.23:4222", ""); cfg.EmbeddedName != "" || name != config.DefaultEmbeddedName {
		t.Fatalf("blank name: config %q label %q, want the default", cfg.EmbeddedName, name)
	}
	a.connNameEd.SetText(strings.Repeat("x", lanNameMax+1))
	if _, err := a.pickerConfig(); err == nil || !strings.Contains(err.Error(), "server name") {
		t.Fatalf("over-long name: err = %v, want one about the server name", err)
	}
	a.connNameEd.SetText(config.DefaultEmbeddedName)
	if got, want := a.pickerHTTPAddr(), a.lanIP+":8080"; got != want {
		t.Fatalf("pickerHTTPAddr = %q, want %q", got, want)
	}

	// Each field is its own port; an emptied one is its default again.
	a.connPortEd.SetText("5222")
	a.connWSPortEd.SetText("5223")
	a.connHTTPPortEd.SetText("")
	cfg, err = a.pickerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmbeddedPort != 5222 || cfg.EmbeddedWSPort != 5223 || cfg.EmbeddedHTTPPort != config.DefaultEmbeddedHTTPPort {
		t.Fatalf("pickerConfig = %+v, want 5222/5223/default", cfg)
	}

	// A bad port names its field; two fields on one port are refused.
	a.connHTTPPortEd.SetText("70000")
	if _, err := a.pickerConfig(); err == nil || !strings.Contains(err.Error(), "HTTP port") {
		t.Fatalf("HTTP port 70000: err = %v, want one naming the HTTP port", err)
	}
	a.connHTTPPortEd.SetText("5223")
	if _, err := a.pickerConfig(); err == nil || !strings.Contains(err.Error(), "must all differ") {
		t.Fatalf("WebSocket and HTTP on one port: err = %v, want must all differ", err)
	}
	// Mid-edit, the advertised addresses fall back to the defaults rather
	// than vanishing.
	a.connPortEd.SetText("x")
	if got := a.pickerAddr(); !strings.HasSuffix(got, ":4222") {
		t.Fatalf("pickerAddr mid-edit = %q, want the default port", got)
	}
}
