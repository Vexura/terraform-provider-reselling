# Every server of the VPC with the label "web".
resource "reselling_load_balancer_target" "web" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "label"
  label            = "web"
}
