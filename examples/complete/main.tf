# A complete setup: a private network with two web servers behind a load balancer,
# a data volume on the first server and a managed PostgreSQL database.
#
#   export RESELLING_API_KEY=rsk_...
#   terraform init && terraform apply
#
# Everything here is billed by the hour while it exists.

terraform {
  required_version = ">= 1.11" # write-only user_data
  required_providers {
    reselling = {
      source  = "vexura/reselling"
      version = "~> 0.1"
    }
  }
}

provider "reselling" {}

# --- lookups -----------------------------------------------------------------

data "reselling_locations" "this" {
  name = var.location
}

data "reselling_os_versions" "debian" {
  name    = "debian"
  version = "12"
}

data "reselling_storage_classes" "this" {
  cluster_id = local.cluster.id
}

data "reselling_load_balancer_plans" "this" {}

data "reselling_database_plans" "this" {}

locals {
  location = data.reselling_locations.this.locations[0]
  cluster  = local.location.clusters[0]
}

# --- access ------------------------------------------------------------------

resource "reselling_ssh_key" "admin" {
  name       = "admin"
  public_key = var.ssh_public_key
}

# --- network -----------------------------------------------------------------

resource "reselling_vpc" "main" {
  name        = "app"
  subnet      = "10.20.0.0/24"
  location_id = local.location.id
}

resource "reselling_firewall_group" "web" {
  name            = "web"
  label_selectors = ["web"]

  rules = [
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "22", source = var.admin_cidr, comment = "ssh" },
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "80", source = "10.20.0.0/24", comment = "from the load balancer" },
    { direction = "in", action = "drop", comment = "everything else" },
  ]
}

# --- servers -----------------------------------------------------------------

resource "reselling_rootserver" "web" {
  count = 2

  cluster_id    = local.cluster.id
  os_version_id = data.reselling_os_versions.debian.versions[0].id
  hostname      = "web${count.index + 1}.${var.domain}"
  cores         = 2
  ram           = 4096
  disk          = 40
  ssh_key_ids   = [reselling_ssh_key.admin.id]
  labels        = ["web"]

  user_data = <<-EOT
    #cloud-config
    packages: [nginx]
    runcmd:
      - echo "ok" > /var/www/html/health
  EOT
}

resource "reselling_vpc_member" "web" {
  count = 2

  vpc_id    = reselling_vpc.main.id
  server_id = reselling_rootserver.web[count.index].id
}

resource "reselling_rootserver_volume" "data" {
  name             = "web1-data"
  storage_class_id = data.reselling_storage_classes.this.storage_classes[0].id
  size_gb          = 50
}

# A separate attachment: the volume survives when the server is replaced.
resource "reselling_rootserver_volume_attachment" "data" {
  volume_id = reselling_rootserver_volume.data.id
  server_id = reselling_rootserver.web[0].id
}

# --- load balancer -----------------------------------------------------------

resource "reselling_load_balancer" "web" {
  name        = "web"
  location_id = local.location.id
  vpc_id      = reselling_vpc.main.id
  plan_id     = data.reselling_load_balancer_plans.this.plans[0].id
}

resource "reselling_load_balancer_certificate" "web" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "managed"
  domains          = [var.domain]
}

resource "reselling_load_balancer_service" "https" {
  load_balancer_id      = reselling_load_balancer.web.id
  name                  = "https"
  protocol              = "https"
  listen_port           = 443
  target_port           = 80
  health_check_protocol = "http"
  health_check_path     = "/health"
  certificate_ids       = [reselling_load_balancer_certificate.web.id]
}

resource "reselling_load_balancer_service" "http" {
  load_balancer_id  = reselling_load_balancer.web.id
  name              = "http"
  protocol          = "http"
  listen_port       = 80
  target_port       = 80
  redirect_to_https = true
}

# Every server of the VPC with the label "web"; new servers join automatically.
resource "reselling_load_balancer_target" "web" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "label"
  label            = "web"

  depends_on = [reselling_vpc_member.web]
}

# --- database ----------------------------------------------------------------

resource "reselling_database" "app" {
  name            = "app-db"
  engine          = "postgresql"
  version         = "17"
  location_id     = local.location.id
  vpc_id          = reselling_vpc.main.id
  plan_id         = data.reselling_database_plans.this.plans[0].id
  trusted_sources = ["10.20.0.0/24"]
}

resource "reselling_database_db" "app" {
  database_id = reselling_database.app.id
  name        = "app"
}

resource "reselling_database_user" "app" {
  database_id = reselling_database.app.id
  username    = "app"
  rotate      = var.db_password_generation

  grants = [{
    db_id     = reselling_database_db.app.id
    privilege = "owner"
  }]
}
