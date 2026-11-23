resource "reselling_vpc_route" "office" {
  vpc_id      = reselling_vpc.backend.id
  destination = "192.168.50.0/24"
  next_hop    = reselling_vpc_member.web.private_ip
  description = "office network via a gateway server"
}
