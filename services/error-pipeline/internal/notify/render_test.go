package notify

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestRenderQuarantineAlertRemovesVictoriaLogsTenantWording(t *testing.T) {
	obs := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	n := RenderQuarantineAlert("proj-pn", "backend", 1, "raw crash password=topsecret", obs)
	if strings.Contains(n.Body, "tenant 0:1") {
		t.Errorf("body still mentions VictoriaLogs tenant 0:1: %q", n.Body)
	}
	if !strings.Contains(n.Body, "logs.proj.quarantine") {
		t.Errorf("body does not mention logs.proj.quarantine: %q", n.Body)
	}
	if strings.Contains(n.Body, "topsecret") {
		t.Errorf("body leaked secret: %q", n.Body)
	}
}

func TestExploreURLAndAttachExploreLink(t *testing.T) {
	obs := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	raw := ExploreURL("http://10.200.0.11:3000/", "proj", "proj-pn", "pn-backend", obs)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	if u.Scheme != "http" || u.Host != "10.200.0.11:3000" || u.Path != "/explore" {
		t.Fatalf("unexpected url: %s", raw)
	}
	var left struct {
		Datasource string `json:"datasource"`
		Queries    []struct {
			Expr string `json:"expr"`
		} `json:"queries"`
		Range struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"range"`
	}
	if err := json.Unmarshal([]byte(u.Query().Get("left")), &left); err != nil {
		t.Fatalf("decode left json: %v", err)
	}
	if left.Datasource != "loki-proj" {
		t.Errorf("datasource = %q, want loki-proj", left.Datasource)
	}
	if len(left.Queries) != 1 || left.Queries[0].Expr != `{k8s_namespace_name="proj-pn",k8s_container_name="pn-backend"}` {
		t.Errorf("queries = %+v", left.Queries)
	}
	if left.Range.From == "" || left.Range.To == "" {
		t.Errorf("range = %+v", left.Range)
	}

	n := AttachExploreLink(Notification{Title: "t", Severity: SeverityError}, "http://10.200.0.11:3000", "quarantine", "proj-pn", "pn-backend", obs)
	if len(n.Fields) != 1 || n.Fields[0].Name != "Grafana" || !strings.Contains(n.Fields[0].Value, "loki-quarantine") {
		t.Errorf("fields = %+v", n.Fields)
	}
	empty := AttachExploreLink(Notification{Title: "t"}, "", "proj", "proj-pn", "pn-backend", obs)
	if len(empty.Fields) != 0 {
		t.Errorf("empty baseURL added field: %+v", empty.Fields)
	}
	for _, bad := range []string{"javascript:alert(1)", "http://admin:secret@10.200.0.11:3000", "ftp://10.200.0.11:3000"} {
		if got := ExploreURL(bad, "proj", "proj-pn", "pn-backend", obs); got != "" {
			t.Errorf("ExploreURL(%q) = %q, want empty", bad, got)
		}
	}
}
