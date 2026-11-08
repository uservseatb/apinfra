server {
    listen $${inboundPort}$$ ssl;
    listen [::]:$${inboundPort}$$ ssl;
    access_log /var/log/nginx/access.log main_ext;

    ssl_certificate $${certSSLDir}$$/fullchain.pem;
    ssl_certificate_key $${certSSLDir}$$/privkey.pem;
    include /etc/nginx/le/options-ssl-nginx.conf;
    ssl_dhparam /etc/nginx/le/ssl-dhparams.pem;

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
