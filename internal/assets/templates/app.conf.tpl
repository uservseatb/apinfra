server {
    listen 80;
    listen [::]:80;
    access_log /var/log/nginx/access.log main_ext;

    server_name $${domain}$$ www.$${domain}$$;

    location /.well-known/acme-challenge/ {
        root /var/www/certbot;
    }

    location / {
        return 301 https://$${domain}$$$request_uri;
    }
}