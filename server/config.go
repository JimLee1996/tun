package main

import (
	"encoding/json"
	"os"
)

// Config for server
type Config struct {
	ListenUDP    string            `json:"listen_udp"`
	ListenTCP    string            `json:"listen_tcp"`
	Listens      map[string]string `json:"listens"`
	Target       string            `json:"target"`
	Key          string            `json:"key"`
	Crypt        string            `json:"crypt"`
	Mode         string            `json:"mode"`
	MTU          int               `json:"mtu"`
	SndWnd       int               `json:"sndwnd"`
	RcvWnd       int               `json:"rcvwnd"`
	DSCP         int               `json:"dscp"`
	AckNodelay   bool              `json:"acknodelay"`
	NoDelay      int               `json:"nodelay"`
	Interval     int               `json:"interval"`
	Resend       int               `json:"resend"`
	NoCongestion int               `json:"nc"`
	SockBuf      int               `json:"sockbuf"`
	KeepAlive    int               `json:"keepalive"`
	Log          string            `json:"log"`
	Quiet        bool              `json:"quiet"`
}

func parseJSONConfig(config *Config, path string) error {
	file, err := os.Open(path) // For read access.
	if err != nil {
		return err
	}
	defer file.Close()

	return json.NewDecoder(file).Decode(config)
}
