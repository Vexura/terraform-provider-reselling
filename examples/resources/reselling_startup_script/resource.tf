resource "reselling_startup_script" "docker" {
  name   = "docker"
  type   = "bash"
  script = <<-EOT
    #!/bin/bash
    apt-get update && apt-get install -y docker.io
  EOT
}
