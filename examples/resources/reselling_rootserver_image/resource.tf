resource "reselling_rootserver_image" "golden" {
  server_id  = reselling_rootserver.web.id
  name       = "web-golden"
  generalize = true
  labels     = ["web"]
}
