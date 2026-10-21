# OpenBao for Bohux secrets (ADR-0008). File storage, and the static seal: the key in
# /etc/bohux/openbao-unseal.key (0400) unseals it after every restart, so a reboot
# never blocks deploys (ADR-0020 item 6). Root on this host can therefore read secret
# values; that was already true through the agent's sealed cache.
storage "file" {
  # The image's own data path: its entrypoint hands it to the openbao user.
  path = "/openbao/file"
}

listener "tcp" {
  address     = "0.0.0.0:8200"
  # Reachable only on the bohux-infra network; TLS arrives with ingress (v0.2).
  tls_disable = true
}

seal "static" {
  current_key_id = "bohux-1"
  current_key    = "file:///openbao/unseal/unseal.key"
}

disable_mlock = true
api_addr      = "http://openbao:8200"
