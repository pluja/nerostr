FROM pluja/strfry:latest

# Install dependencies
RUN apk update
RUN apk add curl jq

# Copy over the plugin
WORKDIR /app
COPY ./strfry.conf /etc/strfry.conf
COPY ./plugin.sh /app/nerostr-auth.sh
RUN chmod +x /app/nerostr-auth.sh