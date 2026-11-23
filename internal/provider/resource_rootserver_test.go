package provider

import (
	"fmt"
	"net/http"
	"sort"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// fakeTasks hands out tasks that are running on the first poll and completed on the second.
type fakeTasks struct {
	polls  map[string]int
	failed map[string]bool
}

func registerTasks(f *fakeAPI) *fakeTasks {
	tasks := &fakeTasks{polls: map[string]int{}, failed: map[string]bool{}}
	f.handle("GET /products/rootserver/tasks/{id}", func(r *fakeRequest) (int, any) {
		id := r.Params["id"]
		if _, ok := tasks.polls[id]; !ok {
			return notFound()
		}
		tasks.polls[id]++
		status := "running"
		if tasks.polls[id] >= 2 {
			status = "completed"
			if tasks.failed[id] {
				return http.StatusOK, map[string]any{"id": id, "type": "x", "status": "failed", "error": map[string]any{"code": "boom", "message": "Es ging schief"}}
			}
		}
		return http.StatusOK, map[string]any{"id": id, "type": "x", "status": status}
	})
	return tasks
}

func (t *fakeTasks) start(f *fakeAPI) map[string]any {
	id := f.id()
	t.polls[id] = 0
	return map[string]any{"id": id, "type": "x", "status": "pending"}
}

type fakeRootservers struct {
	servers map[string]map[string]any
	// gets counts detail reads per server, to simulate the installation.
	gets       map[string]int
	createBody map[string]any
	deleteBody map[string]any
	resizes    []map[string]any
}

func registerRootservers(f *fakeAPI, tasks *fakeTasks) *fakeRootservers {
	rs := &fakeRootservers{servers: map[string]map[string]any{}, gets: map[string]int{}}

	ipv4 := func(n int) []any {
		out := []any{}
		for i := 0; i < n; i++ {
			kind := "secondary"
			if i == 0 {
				kind = "primary"
			}
			out = append(out, map[string]any{"id": fmt.Sprintf("ip-%d", i), "address": fmt.Sprintf("203.0.113.%d", 10+i), "type": kind})
		}
		return out
	}
	ipv6 := func(n int) []any {
		out := []any{}
		for i := 0; i < n; i++ {
			out = append(out, map[string]any{"id": fmt.Sprintf("v6-%d", i), "subnet": fmt.Sprintf("2001:db8:%d::/64", i)})
		}
		return out
	}
	view := func(s map[string]any) map[string]any {
		out := copyMap(s)
		out["ipv4_addresses"] = ipv4(s["_v4"].(int))
		out["ipv6_subnets"] = ipv6(s["_v6"].(int))
		keys := []any{}
		ids := s["_keys"].([]string)
		for _, id := range ids {
			keys = append(keys, map[string]any{"id": id, "name": "key"})
		}
		out["ssh_keys"] = keys
		if s["_v4"].(int) > 0 {
			out["primary_ip"] = "203.0.113.10"
		}
		for k := range out {
			if k[0] == '_' {
				delete(out, k)
			}
		}
		return out
	}

	f.handle("POST /products/rootserver/cluster/{cluster}/create", func(r *fakeRequest) (int, any) {
		if r.Header.Get("Idempotency-Key") == "" {
			f.t.Error("create sent without Idempotency-Key")
		}
		if r.Body["hostname"] == "bad_host" {
			return validationFailed("hostname", "Der Hostname muss ein gültiger Hostname oder FQDN sein.")
		}
		rs.createBody = r.Body
		id := f.id()
		keys := []string{}
		if list, ok := r.Body["ssh_key_ids"].([]any); ok {
			for _, k := range list {
				keys = append(keys, k.(string))
			}
		}
		server := map[string]any{
			"id": id, "host_name": r.Body["hostname"], "labels": []any{}, "notes": r.Body["notes"], "status": "pending",
			"cores": r.Body["cores"], "ram": r.Body["ram"], "disk": r.Body["disk"], "location_id": "loc-1", "cluster_id": r.Params["cluster"],
			"has_user_data": r.Body["user_data"] != nil, "delete_protection": false, "admin_user": "root", "admin_password": "generated-secret",
			"bandwidth_tier_id": r.Body["bandwidth_tier_id"], "startup_script_id": r.Body["startup_script_id"], "image_id": r.Body["image_id"],
			"os":           map[string]any{"id": r.Body["os_version_id"], "name": "Debian"},
			"provisioning": map[string]any{"stage": "preflight", "status": "running"},
			"_v4":          int(r.Body["ipv4_count"].(float64)), "_v6": int(r.Body["ipv6_count"].(float64)), "_keys": keys,
		}
		rs.servers[id] = server
		return http.StatusAccepted, map[string]any{"server": view(server)}
	})
	f.handle("GET /products/rootserver/{id}", func(r *fakeRequest) (int, any) {
		s, ok := rs.servers[r.Params["id"]]
		if !ok {
			return notFound()
		}
		rs.gets[r.Params["id"]]++
		if s["status"] == "pending" && rs.gets[r.Params["id"]] >= 2 {
			s["status"] = "running"
			s["provisioning"] = nil
		}
		return http.StatusOK, view(s)
	})
	f.handle("PATCH /products/rootserver/{id}", func(r *fakeRequest) (int, any) {
		s, ok := rs.servers[r.Params["id"]]
		if !ok {
			return notFound()
		}
		if h, ok := r.Body["hostname"]; ok {
			s["host_name"] = h
		}
		merge(s, r.Body, "labels", "notes")
		return http.StatusOK, map[string]any{"server": view(s)}
	})
	f.handle("POST /products/rootserver/{id}/resize", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		rs.resizes = append(rs.resizes, r.Body)
		merge(s, r.Body, "cores", "ram", "disk", "bandwidth_tier_id")
		return http.StatusAccepted, map[string]any{"task": tasks.start(f), "server": view(s)}
	})
	f.handle("POST /products/rootserver/{id}/ssh-keys", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		s["_keys"] = append(s["_keys"].([]string), r.Body["ssh_key_id"].(string))
		return http.StatusOK, map[string]any{"ssh_keys": []any{}}
	})
	f.handle("DELETE /products/rootserver/{id}/ssh-keys/{key}", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		keep := []string{}
		for _, k := range s["_keys"].([]string) {
			if k != r.Params["key"] {
				keep = append(keep, k)
			}
		}
		s["_keys"] = keep
		return http.StatusOK, map[string]any{"ssh_keys": []any{}}
	})
	f.handle("POST /products/rootserver/{id}/ips/ipv4", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		s["_v4"] = s["_v4"].(int) + int(r.Body["count"].(float64))
		return http.StatusAccepted, map[string]any{"task": tasks.start(f)}
	})
	f.handle("DELETE /products/rootserver/{id}/ips/ipv4/{ip}", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		s["_v4"] = s["_v4"].(int) - 1
		return http.StatusAccepted, map[string]any{"task": tasks.start(f)}
	})
	f.handle("POST /products/rootserver/{id}/reset-root-password", func(r *fakeRequest) (int, any) {
		s := rs.servers[r.Params["id"]]
		s["admin_password"] = r.Body["password"]
		return http.StatusAccepted, map[string]any{"task": tasks.start(f)}
	})
	f.handle("POST /products/rootserver/{id}/delete-protection", func(r *fakeRequest) (int, any) {
		rs.servers[r.Params["id"]]["delete_protection"] = r.Body["enabled"]
		return http.StatusOK, map[string]any{"id": r.Params["id"], "delete_protection": r.Body["enabled"]}
	})
	f.handle("POST /products/rootserver/{id}/delete", func(r *fakeRequest) (int, any) {
		s, ok := rs.servers[r.Params["id"]]
		if !ok {
			return notFound()
		}
		if s["delete_protection"] == true {
			return http.StatusConflict, map[string]any{"success": false, "error": map[string]any{"code": "delete_protected", "message": "Der Löschschutz ist für diesen Server aktiv."}}
		}
		rs.deleteBody = r.Body
		delete(rs.servers, r.Params["id"])
		return http.StatusAccepted, map[string]any{"task": tasks.start(f)}
	})
	return rs
}

func TestRootserverLifecycle(t *testing.T) {
	f := newFakeAPI(t)
	tasks := registerTasks(f)
	rs := registerRootservers(f, tasks)

	config := func(hostname string, cores, disk, ipv4 int, keys string, labels string) string {
		protect := hostname == "web02.example.com" && ipv4 == 2
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_rootserver" "web" {
  delete_protection = %t
  cluster_id    = "cluster-1"
  os_version_id = "os-debian-12"
  hostname      = %q
  cores         = %d
  ram           = 2048
  disk          = %d
  ipv4_count    = %d
  ssh_key_ids   = [%s]
  labels        = [%s]
  user_data     = "#cloud-config\npackages: [nginx]\n"

  volume_on_delete {
    volume_id = "vol-1"
    action    = "delete"
  }

  timeouts {
    create = "1m"
  }
}`, protect, hostname, cores, disk, ipv4, keys, labels)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config("web01.example.com", 2, 40, 1, `"key-a"`, `"web"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver.web", "status", "running"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "location_id", "loc-1"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "primary_ip", "203.0.113.10"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "ipv6_subnets.#", "1"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "admin_password", "generated-secret"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "has_user_data", "true"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "user_data_sha256", sha256Hex("#cloud-config\npackages: [nginx]\n")),
					resource.TestCheckNoResourceAttr("reselling_rootserver.web", "user_data"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "labels.0", "web"),
					func(*terraform.State) error {
						if rs.createBody["user_data"] != "#cloud-config\npackages: [nginx]\n" {
							return fmt.Errorf("user_data was not sent on create: %v", rs.createBody)
						}
						return nil
					},
				),
			},
			{
				// Rename, grow, add a key and an address: all in place.
				Config: config("web02.example.com", 4, 60, 2, `"key-a", "key-b"`, `"web", "prod"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("reselling_rootserver.web", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver.web", "hostname", "web02.example.com"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "cores", "4"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "disk", "60"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "ipv4_addresses.#", "2"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "ssh_key_ids.#", "2"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "labels.#", "2"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "delete_protection", "true"),
					func(*terraform.State) error {
						if len(rs.resizes) != 1 || rs.resizes[0]["cores"] != float64(4) || rs.resizes[0]["disk"] != float64(60) || rs.resizes[0]["ram"] != nil {
							return fmt.Errorf("unexpected resize calls: %v", rs.resizes)
						}
						return nil
					},
				),
			},
			{
				// Fewer addresses, one key less.
				Config: config("web02.example.com", 4, 60, 1, `"key-b"`, `"web", "prod"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("reselling_rootserver.web", "ipv4_count", "1"),
					resource.TestCheckResourceAttr("reselling_rootserver.web", "ssh_key_ids.#", "1"),
				),
			},
			{
				// A smaller disk cannot be resized: the plan replaces the server.
				Config: config("web02.example.com", 4, 50, 1, `"key-b"`, `"web", "prod"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("reselling_rootserver.web", plancheck.ResourceActionReplace)},
				},
			},
			{
				ResourceName:      "reselling_rootserver.web",
				ImportState:       true,
				ImportStateVerify: true,
				// Configuration-only values the API cannot return.
				ImportStateVerifyIgnore: []string{"user_data_sha256", "volume_on_delete", "timeouts"},
			},
		},
	})

	if rs.deleteBody == nil {
		t.Fatal("server was not deleted")
	}
	volumes, _ := rs.deleteBody["volumes"].(map[string]any)
	if volumes["vol-1"] != "delete" {
		t.Fatalf("volume choice not sent on delete: %v", rs.deleteBody)
	}
}

func TestRootserverUserDataChangeReplaces(t *testing.T) {
	f := newFakeAPI(t)
	tasks := registerTasks(f)
	registerRootservers(f, tasks)
	config := func(userData string) string {
		return providerConfig(f) + fmt.Sprintf(`
resource "reselling_rootserver" "web" {
  cluster_id    = "cluster-1"
  os_version_id = "os-debian-12"
  hostname      = "web01"
  cores         = 1
  ram           = 1024
  disk          = 20
  user_data     = %q
}`, userData)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{Config: config("#!/bin/sh\necho one\n")},
			{
				Config: config("#!/bin/sh\necho two\n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("reselling_rootserver.web", plancheck.ResourceActionReplace)},
				},
			},
			{
				// The same content again plans nothing.
				Config:   config("#!/bin/sh\necho two\n"),
				PlanOnly: true,
			},
		},
	})
}

func TestRootserverValidationErrorsPointAtTheAttribute(t *testing.T) {
	f := newFakeAPI(t)
	registerRootservers(f, registerTasks(f))
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(f) + `
resource "reselling_rootserver" "web" {
  cluster_id    = "cluster-1"
  os_version_id = "os-debian-12"
  hostname      = "bad_host"
  cores         = 1
  ram           = 1024
  disk          = 20
}`,
				ExpectError: regexpMust(`(?s)hostname.*gültiger Hostname`),
			},
			{
				Config: providerConfig(f) + `
resource "reselling_rootserver" "web" {
  cluster_id = "cluster-1"
  hostname   = "web"
  cores      = 1
  ram        = 1024
  disk       = 20
}`,
				ExpectError: regexpMust(`Set exactly one of os_version_id and image_id`),
			},
		},
	})
}

func TestRootserverFailedInstallationIsReported(t *testing.T) {
	f := newFakeAPI(t)
	tasks := registerTasks(f)
	rs := registerRootservers(f, tasks)
	// Turn every new server into a failed installation on the first poll.
	f.routes = append([]fakeRoute{{method: "GET", segments: []string{"products", "rootserver", "{id}"}, handler: func(r *fakeRequest) (int, any) {
		s, ok := rs.servers[r.Params["id"]]
		if !ok {
			return notFound()
		}
		s["status"] = "error"
		s["error_message"] = "Kein Platz im Cluster"
		s["error_code"] = "capacity"
		out := copyMap(s)
		for k := range out {
			if k[0] == '_' {
				delete(out, k)
			}
		}
		return http.StatusOK, out
	}}}, f.routes...)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(f) + `
resource "reselling_rootserver" "web" {
  cluster_id    = "cluster-1"
  os_version_id = "os-debian-12"
  hostname      = "web01"
  cores         = 1
  ram           = 1024
  disk          = 20
}`,
			ExpectError: regexpMust(`Kein Platz im Cluster \(capacity\)`),
		}},
	})
	ids := []string{}
	for id := range rs.servers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) != 0 {
		t.Fatalf("the failed server must be tracked and destroyed, left: %v", ids)
	}
}
