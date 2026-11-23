resource "reselling_load_balancer_certificate" "shop" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "managed" # Let's Encrypt
  domains          = ["shop.example.com", "www.shop.example.com"]
}

resource "reselling_load_balancer_certificate" "custom" {
  load_balancer_id = reselling_load_balancer.web.id
  type             = "custom"
  name             = "legacy"
  certificate      = file("cert.pem")
  private_key      = file("key.pem") # sensitive
  chain            = file("chain.pem")
}
