FROM php:8.5-fpm

RUN apt-get update \
    && apt-get install -y nginx git unzip zip curl docker.io libpq-dev \
    && mkdir -p /usr/local/lib/docker/cli-plugins \
    && curl -L "https://github.com/docker/compose/releases/download/v2.31.0/docker-compose-linux-x86_64" -o /usr/local/lib/docker/cli-plugins/docker-compose \
    && chmod +x /usr/local/lib/docker/cli-plugins/docker-compose \
    && rm -rf /var/lib/apt/lists/*

RUN docker-php-ext-install pdo_pgsql

RUN rm -f /etc/nginx/sites-enabled/default /etc/nginx/sites-available/default

RUN curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

WORKDIR /var/www/html
EXPOSE 80

CMD ["/entrypoint.sh"]
