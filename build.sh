#!/bin/bash

UPX=false
if hash upx 2>/dev/null; then
	UPX=true
fi

VERSION=`date -u +%Y%m%d`
LDFLAGS="-X main.VERSION=$VERSION -s -w"
GCFLAGS=""


if [ ! -d "tun" ]; then
  mkdir tun
fi
cd tun
rm -rf *

# Linux x86_64
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o server_linux_amd64 github.com/JimLee1996/tun/server
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_amd64 github.com/JimLee1996/tun/client


# MIPS32LE Client
env CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_mipsle github.com/JimLee1996/tun/client

# ARM Client
env CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_arm  github.com/JimLee1996/tun/client

# macOS Client
env CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_darwin  github.com/JimLee1996/tun/client

if $UPX; then upx -9 *;fi
cp ../*.json .