data "reselling_locations" "fra" {
  name = "Frankfurt"
}

data "reselling_os_versions" "debian" {
  name    = "debian"
  version = "12"
}

resource "reselling_rootserver" "web" {
  cluster_id    = data.reselling_locations.fra.locations[0].clusters[0].id
  os_version_id = data.reselling_os_versions.debian.versions[0].id
  hostname      = "web01.example.com"
  cores         = 2
  ram           = 4096 # MB
  disk          = 40   # GB
  ipv4_count    = 1
  ipv6_count    = 1
  ssh_key_ids   = [reselling_ssh_key.admin.id]
  labels        = ["web"]

  # Write-only (Terraform 1.11+): only its SHA-256 is kept in the state.
  user_data = <<-EOT
    #cloud-config
    packages: [nginx]
  EOT

  # Delete this volume together with the server; volumes not listed are kept.
  volume_on_delete {
    volume_id = reselling_rootserver_volume.data.id
    action    = "delete"
  }

  timeouts {
    create = "45m"
  }
}
