resource "reselling_rootserver_firewall" "web" {
  server_id = reselling_rootserver.web.id

  rules = [
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "9100", source = "10.20.0.0/24", comment = "node exporter" },
  ]
}
