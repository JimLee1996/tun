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

# x86_64
os=linux
arch=amd64
env CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o server_${os}_${arch}${suffix} github.com/JimLee1996/tun/server
env CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_${os}_${arch}${suffix} github.com/JimLee1996/tun/client
if $UPX; then upx -9 client_${os}_${arch}${suffix} server_${os}_${arch}${suffix};fi

# MIPS32LE
env CGO_ENABLED=0 GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_mipsle github.com/JimLee1996/tun/client
if $UPX; then upx -9 client_linux_mips*;fi

# ARM
env CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 go build -ldflags "$LDFLAGS" -gcflags "$GCFLAGS" -o client_linux_arm$v  github.com/JimLee1996/tun/client
if $UPX; then upx -9 client_linux_arm*;fi
