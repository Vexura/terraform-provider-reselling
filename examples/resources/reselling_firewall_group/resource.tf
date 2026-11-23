resource "reselling_firewall_group" "web" {
  name            = "web"
  priority        = 50
  label_selectors = ["web"]
  server_ids      = [reselling_rootserver.web.id]

  rules = [
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "80,443", comment = "http(s)" },
    { direction = "in", action = "accept", protocol = "tcp", destination_port = "22", source = "198.51.100.0/24" },
    { direction = "in", action = "drop" },
  ]
}
