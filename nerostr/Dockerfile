FROM devopsworks/golang-upx:latest as builder
WORKDIR /app
COPY . .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -a -installsuffix cgo -o nerostr . && \
    upx nerostr
RUN chmod +x nerostr

# Tailwind CSS compiler
FROM node:latest as tailwind
WORKDIR /app
COPY ./package.json .
COPY ./package-lock.json .
COPY ./tailwind.config.js .
COPY ./html/ /app/
RUN npm i && \
    npx tailwindcss -i ./static/input.css -o ./style.css --minify

FROM alpine:latest as certs
RUN apk --update add ca-certificates

FROM scratch
COPY --from=certs /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
WORKDIR /app
COPY ./html/ /app/html/
COPY --from=tailwind /app/style.css /app/html/static/style.css
COPY --from=builder /app/nerostr /bin/usr/nerostr
ENV PATH="/bin/usr:${PATH}"
ENTRYPOINT ["nerostr"]