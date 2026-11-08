services:
  webserver:
    image: nginx:1.30-alpine
    container_name: apinfra-proxy
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - ./nginx/conf/:/etc/nginx/conf.d/:ro
      - ./certbot/conf/:/etc/nginx/ssl/:ro
      - ./mockcerts/:/etc/nginx/mockcerts/:ro
      - ./nginx/leconf/:/etc/nginx/le/:ro
      - ./certbot/www/:/var/www/certbot/:ro
      - ./nginx/nginx.conf:/etc/nginx/nginx.conf:ro
