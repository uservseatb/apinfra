server {
    listen $${inboundPort}$$;
    listen [::]:$${inboundPort}$$;
    access_log /var/log/nginx/access.log main_ext;

    location / {
        proxy_pass "$${proxyPass}$$";
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header Host $${proxyHostHeader}$$;
$${proxySSLDirectives}$$
    }
}
