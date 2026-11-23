package provider

import (
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeRuleList is the rule store of one server or group.
type fakeRuleList struct {
	rules []map[string]any
	seq   int
}

func (l *fakeRuleList) list() []any {
	sort.SliceStable(l.rules, func(i, j int) bool { return l.rules[i]["sort_order"].(int) < l.rules[j]["sort_order"].(int) })
	out := []any{}
	for _, r := range l.rules {
		out = append(out, r)
	}
	return out
}

func (l *fakeRuleList) create(f *fakeAPI, body map[string]any) map[string]any {
	l.seq++
	rule := map[string]any{"id": f.id(), "enabled": true, "sort_order": l.seq}
	merge(rule, body, "direction", "action", "protocol", "source", "source_port", "destination", "destination_port", "comment")
	l.rules = append(l.rules, rule)
	return rule
}

func (l *fakeRuleList) find(id string) map[string]any {
	for _, r := range l.rules {
		if r["id"] == id {
			return r
		}
	}
	return nil
}

func (l *fakeRuleList) update(id string, body map[string]any) map[string]any {
	rule := l.find(id)
	if rule == nil {
		return nil
	}
	merge(rule, body, "direction", "action", "protocol", "source", "source_port", "destination", "destination_port", "comment")
	if v, ok := body["is_enabled"]; ok {
		rule["enabled"] = v
	}
	return rule
}

func (l *fakeRuleList) remove(id string) {
	kept := l.rules[:0]
	for _, r := range l.rules {
		if r["id"] != id {
			kept = append(kept, r)
		}
	}
	l.rules = kept
}

func (l *fakeRuleList) reorder(ids []any) {
	for i, id := range ids {
		if r := l.find(id.(string)); r != nil {
			r["sort_order"] = i + 1
		}
	}
}

func registerRuleEndpoints(f *fakeAPI, prefix string, lists func(id string) *fakeRuleList) {
	f.handle("POST "+prefix+"/rules/create", func(r *fakeRequest) (int, any) {
		if r.Body["source"] == "not-an-address" {
			return http.StatusUnprocessableEntity, map[string]any{"success": false, "error": map[string]any{"code": "validation_error", "message": "Quelle ungültig", "details": map[string]any{"source": []string{"Quelle ungültig"}}}}
		}
		return http.StatusCreated, map[string]any{"rule": lists(r.Params["id"]).create(f, r.Body)}
	})
	f.handle("POST "+prefix+"/rules/{rule}/update", func(r *fakeRequest) (int, any) {
		if r.Body["source"] == "not-an-address" {
			return http.StatusUnprocessableEntity, map[string]any{"success": false, "error": map[string]any{"code": "validation_error", "message": "Quelle ungültig", "details": map[string]any{"source": []string{"Quelle ungültig"}}}}
		}
		rule := lists(r.Params["id"]).update(r.Params["rule"], r.Body)
		if rule == nil {
			return notFound()
		}
		return http.StatusOK, map[string]any{"rule": rule}
	})
	f.handle("POST "+prefix+"/rules/{rule}/delete", func(r *fakeRequest) (int, any) {
		lists(r.Params["id"]).remove(r.Params["rule"])
		return http.StatusOK, map[string]any{"message": "gelöscht"}
	})
	f.handle("POST "+prefix+"/rules/reorder", func(r *fakeRequest) (int, any) {
		lists(r.Params["id"]).reorder(r.Body["rule_ids"].([]any))
		return http.StatusOK, map[string]any{"rules": lists(r.Params["id"]).list()}
	})
}

func TestFirewallGroupAndServerFirewall(t *testing.T) {
	f := newFakeAPI(t)
	groups := map[string]map[string]any{}
	groupRules := map[string]*fakeRuleList{}
	serverRules := map[string]*fakeRuleList{"server-1": {}}
	attached := map[string]map[string]bool{}

	groupView := func(id string) map[string]any {
		g := copyMap(groups[id])
		g["rules"] = groupRules[id].list()
		servers := []any{}
		ids := []string{}
		for sid := range attached[id] {
			ids = append(ids, sid)
		}
		sort.Strings(ids)
		for _, sid := range ids {
			servers = append(servers, map[string]any{"id": sid, "attached_explicitly": true, "removing": false})
		}
		g["servers"] = servers
		return g
	}
	f.handle("POST /products/rootserver/firewall-groups/create", func(r *fakeRequest) (int, any) {
		id := f.id()
		groups[id] = map[string]any{"id": id, "name": r.Body["name"], "description": r.Body["description"], "priority": r.Body["priority"], "label_selectors": []any{}}
		groupRules[id] = &fakeRuleList{}
		attached[id] = map[string]bool{}
		return http.StatusCreated, map[string]any{"group": groupView(id)}
	})
	f.handle("GET /products/rootserver/firewall-groups/{id}", func(r *fakeRequest) (int, any) {
		if _, ok := groups[r.Params["id"]]; !ok {
			return notFound()
		}
		return http.StatusOK, map[string]any{"group": groupView(r.Params["id"])}
	})
	f.handle("POST /products/rootserver/firewall-groups/{id}/update", func(r *fakeRequest) (int, any) {
		merge(groups[r.Params["id"]], r.Body, "name", "description", "priority")
		return http.StatusOK, map[string]any{"group": groupView(r.Params["id"])}
	})
	f.handle("POST /products/rootserver/firewall-groups/{id}/delete", func(r *fakeRequest) (int, any) {
		if len(attached[r.Params["id"]]) > 0 && r.Body["confirm"] != true {
			return http.StatusConflict, map[string]any{"success": false, "error": map[string]any{"code": "confirmation_required", "message": "confirm"}}
		}
		delete(groups, r.Params["id"])
		return http.StatusOK, map[string]any{"servers_queued": 0}
	})
	f.handle("POST /products/rootserver/firewall-groups/{id}/servers/attach", func(r *fakeRequest) (int, any) {
		for _, s := range r.Body["server_ids"].([]any) {
			attached[r.Params["id"]][s.(string)] = true
		}
		return http.StatusOK, map[string]any{"group": groupView(r.Params["id"])}
	})
	f.handle("POST /products/rootserver/firewall-groups/{id}/servers/detach", func(r *fakeRequest) (int, any) {
		for _, s := range r.Body["server_ids"].([]any) {
			delete(attached[r.Params["id"]], s.(string))
		}
		return http.StatusOK, map[string]any{"group": groupView(r.Params["id"]), "still_attached_by_label": []any{}}
	})
	f.handle("POST /products/rootserver/firewall-groups/{id}/labels", func(r *fakeRequest) (int, any) {
		groups[r.Params["id"]]["label_selectors"] = r.Body["labels"]
		return http.StatusOK, map[string]any{"group": groupView(r.Params["id"])}
	})
	registerRuleEndpoints(f, "/products/rootserver/firewall-groups/{id}", func(id string) *fakeRuleList { return groupRules[id] })

	f.handle("GET /products/rootserver/{id}/firewall/rules", func(r *fakeRequest) (int, any) {
		l, ok := serverRules[r.Params["id"]]
		if !ok {
			return notFound()
		}
		return http.StatusOK, map[string]any{"rules": l.list(), "groups": []any{}, "effective_rules": []any{}}
	})
	registerRuleEndpoints(f, "/products/rootserver/{id}/firewall", func(id string) *fakeRuleList { return serverRules[id] })

	// A rule that existed before Terraform took over the server firewall: it must be replaced.
	serverRules["server-1"].create(f, map[string]any{"direction": "in", "action": "accept", "destination_port": "8080", "protocol": "tcp"})

	config := func(rules, servers, labels string) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_firewall_group" "web" {
  name            = "web"
  priority        = 50
  server_ids      = [%s]
  label_selectors = [%s]
  rules = [%s]
}

resource "reselling_rootserver_firewall" "server1" {
  server_id = "server-1"
  rules = [
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "22", source = "198.51.100.0/24", comment = "ssh office" },
    { direction = "in", action = "drop" },
  ]
}`, servers, labels, rules)
	}
	httpRule := `{ direction = "in", action = "accept", protocol = "tcp", destination_port = "80" }`
	httpsRule := `{ direction = "in", action = "accept", protocol = "tcp", destination_port = "443" }`
	disabledRule := `{ direction = "in", action = "drop", protocol = "udp", enabled = false }`

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(httpRule+","+httpsRule, `"server-1"`, `"web"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.#", "2"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.1.destination_port", "443"),
					resource.TestCheckResourceAttrSet("reselling_firewall_group.web", "rules.0.id"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "server_ids.#", "1"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "label_selectors.0", "web"),
					resource.TestCheckResourceAttr("reselling_rootserver_firewall.server1", "rules.#", "2"),
					resource.TestCheckResourceAttr("reselling_rootserver_firewall.server1", "rules.0.destination_port", "22"),
					resource.TestCheckResourceAttr("reselling_rootserver_firewall.server1", "rules.1.enabled", "true"),
					func(*terraform.State) error {
						if len(serverRules["server-1"].rules) != 2 {
							return fmt.Errorf("pre-existing server rule not replaced: %v", serverRules["server-1"].rules)
						}
						return nil
					},
				),
			},
			{
				// Swap the order, add a disabled rule, detach the server, drop the label.
				Config: config(httpsRule+","+httpRule+","+disabledRule, ``, ``),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.#", "3"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.0.destination_port", "443"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.2.enabled", "false"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "server_ids.#", "0"),
					resource.TestCheckResourceAttr("reselling_firewall_group.web", "label_selectors.#", "0"),
				),
			},
			{
				Config: config(httpRule, ``, ``),
				Check:  resource.TestCheckResourceAttr("reselling_firewall_group.web", "rules.#", "1"),
			},
			{ResourceName: "reselling_firewall_group.web", ImportState: true, ImportStateVerify: true},
			{ResourceName: "reselling_rootserver_firewall.server1", ImportState: true, ImportStateVerify: true},
			{
				Config: providerConfig(f) + `
resource "reselling_rootserver_firewall" "server1" {
  server_id = "server-1"
  rules     = [{ direction = "in", action = "accept", source = "not-an-address" }]
}`,
				ExpectError: regexpMust(`Quelle ungültig`),
			},
		},
	})
	if len(groups) != 0 {
		t.Fatal("group not deleted")
	}
	if len(serverRules["server-1"].rules) != 0 {
		t.Fatalf("server rules not removed on destroy: %v", serverRules["server-1"].rules)
	}
}
