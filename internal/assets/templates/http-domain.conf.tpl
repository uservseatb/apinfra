server {
    listen 80;
    listen [::]:80;
    access_log /var/log/nginx/access.log main_ext;

    server_name $${domain}$$ www.$${domain}$$;

    location / {
        proxy_pass "http://$${host}$$:$${port}$$";
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $host;
    }
}
