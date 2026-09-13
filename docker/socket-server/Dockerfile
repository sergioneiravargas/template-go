FROM golang:1.25-alpine

COPY bin/socket-server /usr/bin/socket-server
RUN chmod +x /usr/bin/socket-server

WORKDIR /

CMD ["socket-server"]
