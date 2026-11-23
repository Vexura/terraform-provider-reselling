resource "reselling_image_recipe" "base" {
  name          = "base"
  os_version_id = data.reselling_os_versions.debian.versions[0].id
  cores         = 2
  ram           = 2048
  disk          = 20

  step {
    type              = "startup_script"
    startup_script_id = reselling_startup_script.docker.id
  }

  step {
    type    = "user_data"
    content = <<-EOT
      #cloud-config
      packages: [htop]
    EOT
  }
}
