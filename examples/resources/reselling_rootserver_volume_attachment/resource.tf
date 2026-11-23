resource "reselling_rootserver_volume_attachment" "data" {
  volume_id = reselling_rootserver_volume.data.id
  server_id = reselling_rootserver.web.id
}
