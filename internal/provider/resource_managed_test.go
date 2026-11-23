package provider

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestLoadBalancerWithServiceTargetAndCertificate(t *testing.T) {
	f := newFakeAPI(t)
	lbs := map[string]map[string]any{}
	reads := map[string]int{}
	listeners := map[string]map[string]any{}
	targets := map[string]map[string]any{}
	certs := map[string]map[string]any{}
	var certBody map[string]any

	f.handle("POST /products/loadbalancer/create", func(r *fakeRequest) (int, any) {
		lb := map[string]any{
			"id": f.id(), "name": r.Body["name"], "status": "creating", "ipv4_address": "198.51.100.7", "ipv6_address": nil, "private_ip": "10.20.0.250",
			"location": map[string]any{"id": r.Body["location_id"]}, "plan": map[string]any{"id": r.Body["plan_id"]}, "vpc": map[string]any{"id": r.Body["vpc_id"]},
		}
		lbs[lb["id"].(string)] = lb
		return http.StatusCreated, lb
	})
	f.handle("GET /products/loadbalancer/{id}", func(r *fakeRequest) (int, any) {
		lb, ok := lbs[r.Params["id"]]
		if !ok {
			return notFound()
		}
		reads[r.Params["id"]]++
		if reads[r.Params["id"]] >= 2 && lb["status"] == "creating" {
			lb["status"] = "running"
		}
		return http.StatusOK, lb
	})
	f.handle("POST /products/loadbalancer/{id}/update", func(r *fakeRequest) (int, any) {
		lb := lbs[r.Params["id"]]
		if v, ok := r.Body["plan_id"]; ok {
			lb["plan"] = map[string]any{"id": v}
		}
		merge(lb, r.Body, "name")
		return http.StatusOK, lb
	})
	f.handle("POST /products/loadbalancer/{id}/delete", func(r *fakeRequest) (int, any) {
		lbs[r.Params["id"]]["status"] = "pending_deletion"
		return http.StatusOK, lbs[r.Params["id"]]
	})

	defaults := map[string]any{
		"algorithm": "roundrobin", "sticky_sessions": false, "sticky_cookie_name": nil, "proxy_protocol": "none", "health_check_protocol": "tcp",
		"health_check_path": nil, "health_check_expected_status": nil, "health_check_interval": 5, "health_check_timeout": 3, "health_check_rise": 2,
		"health_check_fall": 3, "timeout_connect": 5, "timeout_client": 50, "timeout_server": 50, "redirect_to_https": false, "forward_headers": true,
		"default_certificate_id": nil,
	}
	saveListener := func(l map[string]any, body map[string]any) {
		for k, v := range body {
			l[k] = v
		}
		if ids, ok := body["certificate_ids"].([]any); ok {
			l["certificate_ids"] = ids
		}
	}
	f.handle("POST /products/loadbalancer/{id}/listeners/create", func(r *fakeRequest) (int, any) {
		if r.Body["listen_port"] == float64(25) {
			return validationFailed("listen_port", "Port 25 ist gesperrt.")
		}
		l := copyMap(defaults)
		l["id"] = f.id()
		l["certificate_ids"] = []any{}
		saveListener(l, r.Body)
		listeners[l["id"].(string)] = l
		return http.StatusCreated, l
	})
	f.handle("GET /products/loadbalancer/{id}/listeners", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, l := range listeners {
			list = append(list, l)
		}
		return http.StatusOK, map[string]any{"listeners": list}
	})
	f.handle("POST /products/loadbalancer/{id}/listeners/{l}/update", func(r *fakeRequest) (int, any) {
		saveListener(listeners[r.Params["l"]], r.Body)
		return http.StatusOK, listeners[r.Params["l"]]
	})
	f.handle("POST /products/loadbalancer/{id}/listeners/{l}/delete", func(r *fakeRequest) (int, any) {
		delete(listeners, r.Params["l"])
		return http.StatusOK, nil
	})
	f.handle("POST /products/loadbalancer/{id}/targets/create", func(r *fakeRequest) (int, any) {
		tg := map[string]any{"id": f.id(), "weight": 100, "health": "healthy", "members": []any{map[string]any{"name": "web", "address": "10.20.0.2"}}}
		merge(tg, r.Body, "type", "rootserver_id", "label", "ip_address", "port")
		if w, ok := r.Body["weight"]; ok && w != nil {
			tg["weight"] = w
		}
		targets[tg["id"].(string)] = tg
		return http.StatusCreated, tg
	})
	f.handle("GET /products/loadbalancer/{id}/targets", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, tg := range targets {
			list = append(list, tg)
		}
		return http.StatusOK, map[string]any{"targets": list}
	})
	f.handle("POST /products/loadbalancer/{id}/targets/{t}/update", func(r *fakeRequest) (int, any) {
		merge(targets[r.Params["t"]], r.Body, "port", "weight")
		return http.StatusOK, targets[r.Params["t"]]
	})
	f.handle("POST /products/loadbalancer/{id}/targets/{t}/delete", func(r *fakeRequest) (int, any) {
		delete(targets, r.Params["t"])
		return http.StatusOK, nil
	})
	f.handle("POST /products/loadbalancer/{id}/certificates/create", func(r *fakeRequest) (int, any) {
		certBody = r.Body
		c := map[string]any{"id": f.id(), "name": r.Body["name"], "type": r.Body["type"], "domains": []any{"shop.example.com"}, "status": "active", "fingerprint": "AB:CD", "expires_at": "2027-01-01T00:00:00+00:00"}
		certs[c["id"].(string)] = c
		return http.StatusCreated, c
	})
	f.handle("GET /products/loadbalancer/{id}/certificates", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, c := range certs {
			list = append(list, c)
		}
		return http.StatusOK, map[string]any{"certificates": list}
	})
	f.handle("POST /products/loadbalancer/{id}/certificates/{c}/delete", func(r *fakeRequest) (int, any) {
		delete(certs, r.Params["c"])
		return http.StatusOK, nil
	})

	config := func(plan string, weight int) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_load_balancer" "web" {
  name        = "web-lb"
  location_id = "loc-1"
  vpc_id      = "vpc-1"
  plan_id     = %q
}

resource "reselling_load_balancer_certificate" "shop" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "custom"
  name             = "shop"
  certificate      = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"
  private_key      = "-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"
}

resource "reselling_load_balancer_service" "https" {
  load_balancer_id  = reselling_load_balancer.web.id
  name              = "https"
  protocol          = "https"
  listen_port       = 443
  target_port       = 8080
  health_check_path = "/health"
  certificate_ids   = [reselling_load_balancer_certificate.shop.id]
}

resource "reselling_load_balancer_target" "web" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "label"
  label            = "web"
  weight           = %d
}`, plan, weight)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("plan-small", 100),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_load_balancer.web", "status", "running"),
					resource.TestCheckResourceAttr("reselling_load_balancer.web", "ipv4_address", "198.51.100.7"),
					resource.TestCheckResourceAttr("reselling_load_balancer.web", "enable_ipv6", "false"),
					resource.TestCheckResourceAttr("reselling_load_balancer_service.https", "algorithm", "roundrobin"),
					resource.TestCheckResourceAttr("reselling_load_balancer_service.https", "certificate_ids.#", "1"),
					resource.TestCheckResourceAttr("reselling_load_balancer_target.web", "members.0", "10.20.0.2"),
					resource.TestCheckResourceAttr("reselling_load_balancer_certificate.shop", "fingerprint", "AB:CD"),
					func(*terraform.State) error {
						if certBody["private_key"] == nil || certBody["certificate"] == nil {
							return fmt.Errorf("PEM fields not sent: %v", certBody)
						}
						return nil
					},
				),
			},
			{
				Config: config("plan-large", 50),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_load_balancer.web", "plan_id", "plan-large"),
					resource.TestCheckResourceAttr("reselling_load_balancer_target.web", "weight", "50"),
				),
			},
			{ResourceName: "reselling_load_balancer.web", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "reselling_load_balancer_service.https", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: childImportID("reselling_load_balancer_service.https", "load_balancer_id")},
			{ResourceName: "reselling_load_balancer_target.web", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: childImportID("reselling_load_balancer_target.web", "load_balancer_id")},
			{
				ResourceName: "reselling_load_balancer_certificate.shop", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       childImportID("reselling_load_balancer_certificate.shop", "load_balancer_id"),
				ImportStateVerifyIgnore: []string{"certificate", "private_key", "chain"},
			},
			{
				Config: config("plan-large", 50) + `
resource "reselling_load_balancer_service" "smtp" {
  load_balancer_id = reselling_load_balancer.web.id
  name             = "smtp"
  protocol         = "tcp"
  listen_port      = 25
  target_port      = 25
}`,
				ExpectError: regexpMust(`(?s)listen_port.*Port 25 ist gesperrt`),
			},
		},
	})
	for id, lb := range lbs {
		if lb["status"] != "pending_deletion" {
			t.Fatalf("load balancer %s not deleted", id)
		}
	}
	if len(listeners)+len(targets)+len(certs) != 0 {
		t.Fatal("children not deleted")
	}
}

func TestDatabaseWithDBAndUser(t *testing.T) {
	f := newFakeAPI(t)
	dbs := map[string]map[string]any{}
	reads := map[string]int{}
	logical := map[string]map[string]any{}
	users := map[string]map[string]any{}
	rotations := 0

	view := func(d map[string]any) map[string]any {
		out := copyMap(d)
		return out
	}
	f.handle("POST /products/database/create", func(r *fakeRequest) (int, any) {
		d := map[string]any{
			"id": f.id(), "name": r.Body["name"], "engine": r.Body["engine"], "version": r.Body["version"], "status": "creating",
			"host": "10.20.0.30", "port": 5432, "location": map[string]any{"id": r.Body["location_id"]}, "vpc": map[string]any{"id": r.Body["vpc_id"]},
			"plan": map[string]any{"id": r.Body["plan_id"]}, "storage_class": map[string]any{"id": "sc-ssd"}, "deletion_requested": false,
			"trusted_sources": r.Body["trusted_sources"],
			"public_access":   map[string]any{"enabled": false, "ipv6": false, "sources": []any{}, "allow_any": false, "addresses": []any{}},
			"maintenance":     map[string]any{"day": 7, "hour": 3, "auto_minor_updates": true},
		}
		if d["trusted_sources"] == nil {
			d["trusted_sources"] = []any{}
		}
		dbs[d["id"].(string)] = d
		return http.StatusCreated, view(d)
	})
	f.handle("GET /products/database/{id}", func(r *fakeRequest) (int, any) {
		d, ok := dbs[r.Params["id"]]
		if !ok {
			return notFound()
		}
		reads[r.Params["id"]]++
		if reads[r.Params["id"]] >= 2 && d["status"] == "creating" {
			d["status"] = "running"
		}
		return http.StatusOK, view(d)
	})
	f.handle("POST /products/database/{id}/update", func(r *fakeRequest) (int, any) {
		d := dbs[r.Params["id"]]
		merge(d, r.Body, "name", "trusted_sources")
		if v, ok := r.Body["plan_id"]; ok {
			d["plan"] = map[string]any{"id": v}
		}
		if v, ok := r.Body["public_access"]; ok {
			addresses := []any{}
			if v == true {
				addresses = []any{"203.0.113.50"}
			}
			d["public_access"] = map[string]any{"enabled": v, "ipv6": r.Body["public_ipv6"], "sources": r.Body["public_sources"], "allow_any": r.Body["public_allow_any"], "addresses": addresses}
		}
		m := d["maintenance"].(map[string]any)
		if v, ok := r.Body["maintenance_day"]; ok {
			m["day"] = v
		}
		if v, ok := r.Body["maintenance_hour"]; ok {
			m["hour"] = v
		}
		return http.StatusOK, view(d)
	})
	f.handle("POST /products/database/{id}/delete", func(r *fakeRequest) (int, any) {
		dbs[r.Params["id"]]["deletion_requested"] = true
		return http.StatusOK, view(dbs[r.Params["id"]])
	})
	f.handle("POST /products/database/{id}/databases/create", func(r *fakeRequest) (int, any) {
		db := map[string]any{"id": f.id(), "name": r.Body["name"]}
		logical[db["id"].(string)] = db
		return http.StatusCreated, db
	})
	f.handle("GET /products/database/{id}/databases", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, db := range logical {
			list = append(list, db)
		}
		return http.StatusOK, map[string]any{"databases": list}
	})
	f.handle("POST /products/database/{id}/databases/{db}/delete", func(r *fakeRequest) (int, any) {
		delete(logical, r.Params["db"])
		return http.StatusOK, map[string]any{"deleted": true}
	})
	f.handle("POST /products/database/{id}/users/create", func(r *fakeRequest) (int, any) {
		u := map[string]any{"id": f.id(), "username": r.Body["username"], "grants": r.Body["grants"]}
		users[u["id"].(string)] = u
		return http.StatusCreated, map[string]any{"user": u, "password": "first-password"}
	})
	f.handle("GET /products/database/{id}/users", func(r *fakeRequest) (int, any) {
		list := []any{}
		for _, u := range users {
			list = append(list, u)
		}
		return http.StatusOK, map[string]any{"users": list}
	})
	f.handle("POST /products/database/{id}/users/{u}/update", func(r *fakeRequest) (int, any) {
		users[r.Params["u"]]["grants"] = r.Body["grants"]
		return http.StatusOK, users[r.Params["u"]]
	})
	f.handle("POST /products/database/{id}/users/{u}/rotate-password", func(r *fakeRequest) (int, any) {
		rotations++
		return http.StatusOK, map[string]any{"username": users[r.Params["u"]]["username"], "password": fmt.Sprintf("rotated-%d", rotations)}
	})
	f.handle("POST /products/database/{id}/users/{u}/delete", func(r *fakeRequest) (int, any) {
		delete(users, r.Params["u"])
		return http.StatusOK, map[string]any{"deleted": true}
	})

	config := func(public bool, privilege, rotate string) string {
		publicBlock := ""
		if public {
			publicBlock = `
  public_access  = true
  public_sources = ["203.0.113.10/32"]`
		}
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_database" "shop" {
  name            = "shop-db"
  engine          = "postgresql"
  version         = "17"
  location_id     = "loc-1"
  vpc_id          = "vpc-1"
  plan_id         = "db-small"
  trusted_sources = ["10.20.0.0/26"]
  maintenance_day = 2
  %s
}

resource "reselling_database_db" "shop" {
  database_id = reselling_database.shop.id
  name        = "shop"
}

resource "reselling_database_user" "app" {
  database_id = reselling_database.shop.id
  username    = "shop_app"
  rotate      = %q
  grants = [{
    db_id     = reselling_database_db.shop.id
    privilege = %q
  }]
}`, publicBlock, rotate, privilege)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config(false, "owner", "v1"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_database.shop", "status", "running"),
					resource.TestCheckResourceAttr("reselling_database.shop", "host", "10.20.0.30"),
					resource.TestCheckResourceAttr("reselling_database.shop", "maintenance_day", "2"),
					resource.TestCheckResourceAttr("reselling_database.shop", "maintenance_hour", "3"),
					resource.TestCheckResourceAttr("reselling_database_user.app", "password", "first-password"),
					resource.TestCheckResourceAttr("reselling_database_user.app", "grants.#", "1"),
				),
			},
			{
				Config: config(true, "read_only", "v2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_database.shop", "public_access", "true"),
					resource.TestCheckResourceAttr("reselling_database.shop", "public_addresses.0", "203.0.113.50"),
					resource.TestCheckResourceAttr("reselling_database_user.app", "password", "rotated-1"),
				),
			},
			{ResourceName: "reselling_database.shop", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"timeouts"}},
			{ResourceName: "reselling_database_db.shop", ImportState: true, ImportStateVerify: true, ImportStateIdFunc: childImportID("reselling_database_db.shop", "database_id")},
			{
				ResourceName: "reselling_database_user.app", ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc:       childImportID("reselling_database_user.app", "database_id"),
				ImportStateVerifyIgnore: []string{"password", "rotate"},
			},
		},
	})
	if rotations != 1 {
		t.Fatalf("expected exactly one rotation, got %d", rotations)
	}
	for _, d := range dbs {
		if d["deletion_requested"] != true {
			t.Fatal("database not deleted")
		}
	}
}
