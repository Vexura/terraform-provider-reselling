resource "reselling_load_balancer_service" "https" {
  load_balancer_id      = reselling_load_balancer.web.id
  name                  = "https"
  protocol              = "https"
  listen_port           = 443
  target_port           = 80
  health_check_protocol = "http"
  health_check_path     = "/health"
  redirect_to_https     = true
  certificate_ids       = [reselling_load_balancer_certificate.shop.id]
}
