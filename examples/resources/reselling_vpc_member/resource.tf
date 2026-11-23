resource "reselling_vpc_member" "web" {
  vpc_id     = reselling_vpc.backend.id
  server_id  = reselling_rootserver.web.id
  private_ip = "10.20.0.10" # optional; the lowest free address otherwise
}
