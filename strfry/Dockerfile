FROM pluja/strfry:latest

# Install dependencies
RUN apt update
RUN apt install curl jq -y

# Copy over the plugin
WORKDIR /app
COPY ./strfry.conf /etc/strfry.conf
COPY ./plugin.sh /app/nerostr-auth.sh
RUN chmod +x /app/nerostr-auth.sh