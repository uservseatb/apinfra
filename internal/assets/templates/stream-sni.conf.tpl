map $ssl_preread_server_name $backend {
    hostnames;
$${mapEntries}$$
}

server {
    listen 443;
    listen [::]:443;
    ssl_preread on;
    proxy_connect_timeout 10s;
    proxy_timeout 1d;
    proxy_pass $backend;
}
