package provider

import (
	"net/http"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestDataSources(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /products/rootserver/locations", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"locations": []any{
			map[string]any{"id": "loc-fra", "name": "Frankfurt", "city": "Frankfurt am Main", "country_code": "DE", "clusters": []any{
				map[string]any{"id": "cluster-epyc", "name": "Epyc", "system_info": map[string]any{"cpu": "AMD Epyc 9654"}, "min_cores": 1, "max_cores": 32,
					"min_ram": 1024, "max_ram": 65536, "min_disk": 10, "max_disk": 500, "disk_steps": 10, "max_ipv4": 4, "max_ipv6": 4,
					"bandwidth_tiers": []any{map[string]any{"id": "bw-10", "name": "10 Gbit/s", "speed_mbps": 10000, "is_default": true}}},
			}},
			map[string]any{"id": "loc-dus", "name": "Düsseldorf", "city": "Düsseldorf", "country_code": "DE", "clusters": []any{}},
		}}
	})
	f.handle("GET /products/rootserver/os-list", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"operating_systems": []any{
				map[string]any{"name": "Debian", "versions": []any{map[string]any{"id": "deb-12", "version": "12", "min_cores": 1, "min_ram_gb": 1, "min_disk_gb": 10}, map[string]any{"id": "deb-13", "version": "13"}}},
				map[string]any{"name": "Ubuntu", "versions": []any{map[string]any{"id": "ubu-24", "version": "24.04"}}},
			},
			"apps": []any{map[string]any{"name": "Coolify", "version_id": "app-coolify"}},
		}
	})
	f.handle("GET /products/rootserver/images", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"images": []any{
			map[string]any{"id": "img-1", "name": "golden", "status": "available", "os": map[string]any{"id": "deb-12"}, "cluster": map[string]any{"id": "cluster-epyc"}, "location": map[string]any{"id": "loc-fra"}, "size_gb": 4, "min_disk_gb": 20, "owned": true, "labels": []any{"web"}},
			map[string]any{"id": "img-2", "name": "other", "status": "available", "owned": false},
		}}
	})
	f.handle("GET /products/rootserver/volumes/storage-classes/{cluster}", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"storage_classes": []any{map[string]any{"id": "sc-ssd", "name": "SSD", "is_ssd": true, "min_size_gb": 10, "max_size_gb": 2000, "step_gb": 10, "price_per_gb_monthly": 0.08}}}
	})
	f.handle("GET /products/loadbalancer/plans", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"plans": []any{map[string]any{"id": "lb-s", "name": "lb-small", "display_name": "LB S", "cores": 1, "memory_mb": 1024, "monthly_price": 5.9}}}
	})
	f.handle("GET /products/database/plans", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{
			"plans":   []any{map[string]any{"id": "db-s", "name": "db-small", "cores": 2, "memory_mb": 4096, "disk_gb": 40, "monthly_price": 19}, map[string]any{"id": "db-l", "name": "db-large", "cores": 8}},
			"engines": []any{map[string]any{"engine": "postgresql", "version": "17", "label": "PostgreSQL 17"}},
		}
	})
	f.handle("GET /products/kubernetes/plans", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"plans": []any{map[string]any{"id": "cp", "name": "cp-small", "cores": 2, "ram_mb": 4096, "disk_gb": 40, "is_control_plane": true, "price_monthly": 12.5}}}
	})
	f.handle("GET /products/kubernetes/versions", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"versions": []any{map[string]any{"id": "v131", "version": "1.31", "is_default": true}}}
	})
	server := map[string]any{"id": "srv-1", "host_name": "web01.example.com", "status": "running", "cores": 2, "ram": 2048, "disk": 40, "primary_ip": "203.0.113.10",
		"location_id": "loc-fra", "cluster_id": "cluster-epyc", "labels": []any{"web"}, "os": map[string]any{"id": "deb-12"},
		"ipv4_addresses": []any{map[string]any{"id": "ip-1", "address": "203.0.113.10", "type": "primary"}}, "ipv6_subnets": []any{}}
	f.handle("GET /products/rootserver", func(r *fakeRequest) (int, any) {
		if r.URL.Query().Get("search") != "web01.example.com" {
			f.t.Errorf("hostname lookup did not search: %s", r.URL.RawQuery)
		}
		return http.StatusOK, map[string]any{"servers": []any{server, map[string]any{"id": "srv-2", "host_name": "web01.example.com.old"}}}
	})
	f.handle("GET /products/rootserver/{id}", func(r *fakeRequest) (int, any) {
		if r.Params["id"] != "srv-1" {
			return notFound()
		}
		return http.StatusOK, server
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(f) + `
data "reselling_locations" "fra" {
  name = "frankfurt"
}
data "reselling_os_versions" "debian12" {
  name    = "debian"
  version = "12"
}
data "reselling_os_versions" "all" {}
data "reselling_images" "golden" {
  name = "gold"
}
data "reselling_storage_classes" "epyc" {
  cluster_id = data.reselling_locations.fra.locations[0].clusters[0].id
}
data "reselling_load_balancer_plans" "all" {}
data "reselling_database_plans" "small" {
  name = "small"
}
data "reselling_kubernetes_plans" "all" {}
data "reselling_kubernetes_versions" "all" {}
data "reselling_rootserver" "by_name" {
  hostname = "web01.example.com"
}
data "reselling_rootserver" "by_id" {
  id = "srv-1"
}`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.reselling_locations.fra", "locations.#", "1"),
				resource.TestCheckResourceAttr("data.reselling_locations.fra", "locations.0.clusters.0.max_cores", "32"),
				resource.TestCheckResourceAttr("data.reselling_locations.fra", "locations.0.clusters.0.cpu", "AMD Epyc 9654"),
				resource.TestCheckResourceAttr("data.reselling_locations.fra", "locations.0.clusters.0.bandwidth_tiers.0.id", "bw-10"),
				resource.TestCheckResourceAttr("data.reselling_os_versions.debian12", "versions.#", "1"),
				resource.TestCheckResourceAttr("data.reselling_os_versions.debian12", "versions.0.id", "deb-12"),
				resource.TestCheckResourceAttr("data.reselling_os_versions.all", "versions.#", "4"),
				resource.TestCheckResourceAttr("data.reselling_os_versions.all", "versions.3.type", "app"),
				resource.TestCheckResourceAttr("data.reselling_images.golden", "images.#", "1"),
				resource.TestCheckResourceAttr("data.reselling_images.golden", "images.0.os_version_id", "deb-12"),
				resource.TestCheckResourceAttr("data.reselling_storage_classes.epyc", "storage_classes.0.price_per_gb_monthly", "0.08"),
				resource.TestCheckResourceAttr("data.reselling_load_balancer_plans.all", "plans.0.monthly_price", "5.9"),
				resource.TestCheckResourceAttr("data.reselling_database_plans.small", "plans.#", "1"),
				resource.TestCheckResourceAttr("data.reselling_database_plans.small", "engines.0.version", "17"),
				resource.TestCheckResourceAttr("data.reselling_kubernetes_plans.all", "plans.0.memory_mb", "4096"),
				resource.TestCheckResourceAttr("data.reselling_kubernetes_plans.all", "plans.0.is_control_plane", "true"),
				resource.TestCheckResourceAttr("data.reselling_kubernetes_plans.all", "plans.0.monthly_price", "12.5"),
				resource.TestCheckResourceAttr("data.reselling_kubernetes_versions.all", "default_id", "v131"),
				resource.TestCheckResourceAttr("data.reselling_rootserver.by_name", "id", "srv-1"),
				resource.TestCheckResourceAttr("data.reselling_rootserver.by_name", "ipv4_addresses.0", "203.0.113.10"),
				resource.TestCheckResourceAttr("data.reselling_rootserver.by_id", "hostname", "web01.example.com"),
			),
		}},
	})
}

func TestRootserverDataSourceUnknownHostname(t *testing.T) {
	f := newFakeAPI(t)
	f.handle("GET /products/rootserver", func(r *fakeRequest) (int, any) {
		return http.StatusOK, map[string]any{"servers": []any{}}
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(f) + `
data "reselling_rootserver" "missing" {
  hostname = "nope.example.com"
}`,
			ExpectError: regexpMust(`No rootserver has the hostname`),
		}},
	})
}
