package tools

import (
	"strings"
	"testing"
)

func TestValidateCustomCompose(t *testing.T) {
	svc := func(extra map[string]interface{}) map[string]interface{} {
		s := map[string]interface{}{"image": "nginx:1.27"}
		for k, v := range extra {
			s[k] = v
		}
		return map[string]interface{}{"services": map[string]interface{}{"web": s}}
	}

	tests := []struct {
		name         string
		compose      map[string]interface{}
		wantErr      string
		wantWarnings int
	}{
		{"minimal", svc(nil), "", 0},
		{"no services", map[string]interface{}{}, "non-empty services", 0},
		{"no image", map[string]interface{}{"services": map[string]interface{}{"web": map[string]interface{}{}}}, "needs an image", 0},
		{"bind under /mnt", svc(map[string]interface{}{"volumes": []interface{}{"/mnt/Apps/web:/data"}}), "", 0},
		{"named volume", svc(map[string]interface{}{"volumes": []interface{}{"webdata:/data"}}), "", 0},
		{"bind outside /mnt", svc(map[string]interface{}{"volumes": []interface{}{"/var/run/docker.sock:/var/run/docker.sock"}}), "must be under /mnt/", 0},
		{"relative bind", svc(map[string]interface{}{"volumes": []interface{}{"./data:/data"}}), "must be under /mnt/", 0},
		{"long-form bind outside /mnt", svc(map[string]interface{}{"volumes": []interface{}{
			map[string]interface{}{"type": "bind", "source": "/etc", "target": "/host-etc"}}}), "must be under /mnt/", 0},
		{"long-form bind under /mnt", svc(map[string]interface{}{"volumes": []interface{}{
			map[string]interface{}{"type": "bind", "source": "/mnt/Apps/web", "target": "/data"}}}), "", 0},
		{"privileged and host network warn", svc(map[string]interface{}{"privileged": true, "network_mode": "host"}), "", 2},
		{"devices and caps warn", svc(map[string]interface{}{"devices": []interface{}{"/dev/dri"}, "cap_add": []interface{}{"NET_ADMIN"}}), "", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warnings, err := validateCustomCompose(tt.compose)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(warnings) != tt.wantWarnings {
				t.Fatalf("want %d warnings, got %v", tt.wantWarnings, warnings)
			}
		})
	}
}

func TestParseCustomAppArgs(t *testing.T) {
	compose := map[string]interface{}{"services": map[string]interface{}{}}
	if _, _, _, err := parseCustomAppArgs(map[string]interface{}{"compose": compose}); err == nil {
		t.Error("missing app_name should fail")
	}
	if _, _, _, err := parseCustomAppArgs(map[string]interface{}{"app_name": "Bad_Name", "compose": compose}); err == nil {
		t.Error("invalid app_name should fail")
	}
	if _, _, _, err := parseCustomAppArgs(map[string]interface{}{"app_name": "web", "compose": "services: {}"}); err == nil {
		t.Error("string compose should fail")
	}
	name, _, update, err := parseCustomAppArgs(map[string]interface{}{"app_name": "web", "compose": compose, "update": true})
	if err != nil || name != "web" || !update {
		t.Errorf("valid args: got %q %v %v", name, update, err)
	}
}
