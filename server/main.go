package main

import (
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"

	"github.com/JimLee1996/tun/kcp"
	"github.com/JimLee1996/tun/smux"
	"github.com/JimLee1996/tun/tcpraw"
	"github.com/urfave/cli"
)

// VERSION is injected by buildflags
var VERSION = "SELFBUILD"

// handle multiplex-ed connection
func handleMux(conn io.ReadWriteCloser, config *Config) {
	// stream multiplex
	smuxConfig := smux.DefaultConfig()
	smuxConfig.MaxReceiveBuffer = config.SockBuf
	smuxConfig.KeepAliveInterval = time.Duration(config.KeepAlive) * time.Second

	mux, err := smux.Server(conn, smuxConfig)
	if err != nil {
		log.Println(err)
		return
	}
	defer mux.Close()
	for {
		stream, err := mux.AcceptStream()
		if err != nil {
			log.Println(err)
			return
		}

		go func(p1 *smux.Stream) {
			p2, err := net.Dial("tcp", config.Target)
			if err != nil {
				p1.Close()
				log.Println(err)
				return
			}
			handleClient(p1, p2, config.Quiet)
		}(stream)
	}
}

func handleClient(p1, p2 io.ReadWriteCloser, quiet bool) {
	if !quiet {
		log.Println("stream opened")
		defer log.Println("stream closed")
	}
	defer p1.Close()
	defer p2.Close()

	// start tunnel
	p1die := make(chan struct{})
	buf1 := make([]byte, 65535)
	go func() { io.CopyBuffer(p1, p2, buf1); close(p1die) }()

	p2die := make(chan struct{})
	buf2 := make([]byte, 65535)
	go func() { io.CopyBuffer(p2, p1, buf2); close(p2die) }()

	// wait for tunnel termination
	select {
	case <-p1die:
	case <-p2die:
	}
}

func checkError(err error) {
	if err != nil {
		log.Printf("%+v\n", err)
		os.Exit(-1)
	}
}

func main() {
	rand.Seed(int64(time.Now().Nanosecond()))
	if VERSION == "SELFBUILD" {
		// add more log flags for debugging
		log.SetFlags(log.LstdFlags | log.Lshortfile)
	}
	myApp := cli.NewApp()
	myApp.Name = "kcptun"
	myApp.Usage = "server(with SMUX)"
	myApp.Version = VERSION
	myApp.Flags = []cli.Flag{
		cli.StringFlag{
			Name:  "listen_udp,lu",
			Value: ":18388",
			Usage: "kcp server listen (UDP) address",
		},
		cli.StringFlag{
			Name:  "listen_tcp, lt",
			Value: ":1935",
			Usage: "kcp server listen (TCP) address",
		},
		cli.StringFlag{
			Name:  "target, t",
			Value: "127.0.0.1:8388",
			Usage: "target server address",
		},
		cli.StringFlag{
			Name:  "mode",
			Value: "fast3",
			Usage: "profiles: fast3, fast2, fast, normal, manual",
		},
		cli.IntFlag{
			Name:  "mtu",
			Value: 1350,
			Usage: "set maximum transmission unit for UDP packets",
		},
		cli.IntFlag{
			Name:  "sndwnd",
			Value: 1024,
			Usage: "set send window size(num of packets)",
		},
		cli.IntFlag{
			Name:  "rcvwnd",
			Value: 1024,
			Usage: "set receive window size(num of packets)",
		},
		cli.IntFlag{
			Name:  "dscp",
			Value: 0,
			Usage: "set DSCP(6bit)",
		},
		cli.BoolFlag{
			Name:   "acknodelay",
			Usage:  "flush ack immediately when a packet is received",
			Hidden: true,
		},
		cli.IntFlag{
			Name:   "nodelay",
			Value:  0,
			Hidden: true,
		},
		cli.IntFlag{
			Name:   "interval",
			Value:  50,
			Hidden: true,
		},
		cli.IntFlag{
			Name:   "resend",
			Value:  0,
			Hidden: true,
		},
		cli.IntFlag{
			Name:   "nc",
			Value:  0,
			Hidden: true,
		},
		cli.IntFlag{
			Name:  "sockbuf",
			Value: 4194304, // socket buffer size in bytes
			Usage: "per-socket buffer in bytes",
		},
		cli.IntFlag{
			Name:  "keepalive",
			Value: 10, // nat keepalive interval in seconds
			Usage: "seconds between heartbeats",
		},
		cli.StringFlag{
			Name:  "log",
			Value: "",
			Usage: "specify a log file to output, default goes to stderr",
		},
		cli.BoolFlag{
			Name:  "quiet",
			Usage: "to suppress the 'stream open/close' messages",
		},
		cli.StringFlag{
			Name:  "c",
			Value: "", // when the value is not empty, the config path must exists
			Usage: "config from json file, which will override the command from shell",
		},
	}
	myApp.Action = func(c *cli.Context) error {
		config := Config{}
		config.ListenUDP = c.String("listen_udp")
		config.ListenTCP = c.String("listen_tcp")
		config.Target = c.String("target")
		config.Mode = c.String("mode")
		config.MTU = c.Int("mtu")
		config.SndWnd = c.Int("sndwnd")
		config.RcvWnd = c.Int("rcvwnd")
		config.DSCP = c.Int("dscp")
		config.AckNodelay = c.Bool("acknodelay")
		config.NoDelay = c.Int("nodelay")
		config.Interval = c.Int("interval")
		config.Resend = c.Int("resend")
		config.NoCongestion = c.Int("nc")
		config.SockBuf = c.Int("sockbuf")
		config.KeepAlive = c.Int("keepalive")
		config.Log = c.String("log")
		config.Quiet = c.Bool("quiet")

		if c.String("c") != "" {
			//Now only support json config file
			err := parseJSONConfig(&config, c.String("c"))
			checkError(err)
		}

		// log redirect
		if config.Log != "" {
			f, err := os.OpenFile(config.Log, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
			checkError(err)
			defer f.Close()
			log.SetOutput(f)
		}

		switch config.Mode {
		case "normal":
			config.NoDelay, config.Interval, config.Resend, config.NoCongestion = 0, 40, 2, 1
		case "fast":
			config.NoDelay, config.Interval, config.Resend, config.NoCongestion = 0, 30, 2, 1
		case "fast2":
			config.NoDelay, config.Interval, config.Resend, config.NoCongestion = 1, 20, 2, 1
		case "fast3":
			config.NoDelay, config.Interval, config.Resend, config.NoCongestion = 1, 10, 2, 1
		}

		log.Println("target:", config.Target)
		log.Println("nodelay parameters:", config.NoDelay, config.Interval, config.Resend, config.NoCongestion)
		log.Println("sndwnd:", config.SndWnd, "rcvwnd:", config.RcvWnd)
		log.Println("mtu:", config.MTU)
		log.Println("acknodelay:", config.AckNodelay)
		log.Println("dscp:", config.DSCP)
		log.Println("sockbuf:", config.SockBuf)
		log.Println("keepalive:", config.KeepAlive)
		log.Println("quiet:", config.Quiet)

		// main loop
		var wg sync.WaitGroup
		loop := func(lis *kcp.Listener) {
			defer wg.Done()

			if err := lis.SetDSCP(config.DSCP); err != nil {
				log.Println("SetDSCP:", err)
			}
			if err := lis.SetReadBuffer(config.SockBuf); err != nil {
				log.Println("SetReadBuffer:", err)
			}
			if err := lis.SetWriteBuffer(config.SockBuf); err != nil {
				log.Println("SetWriteBuffer:", err)
			}

			for {
				if conn, err := lis.AcceptKCP(); err == nil {
					log.Println("remote address:", conn.RemoteAddr())
					conn.SetStreamMode(true)
					conn.SetWriteDelay(false)
					conn.SetNoDelay(config.NoDelay, config.Interval, config.Resend, config.NoCongestion)
					conn.SetMtu(config.MTU)
					conn.SetWindowSize(config.SndWnd, config.RcvWnd)
					conn.SetACKNoDelay(config.AckNodelay)
					go handleMux(conn, &config)
				} else {
					log.Printf("%+v", err)
				}
			}
		}

		// listen multiple ports
		if len(config.Listens) != 0 {
			for addr, protocol := range config.Listens {
				if protocol == "tcp" {
					log.Println("listening (tcp) on:", addr)
					if conn, err := tcpraw.Listen("tcp", addr); err == nil {
						lis, err := kcp.ServeConn(conn)
						checkError(err)
						wg.Add(1)
						go loop(lis)
					} else {
						log.Println(err)
					}
				} else if protocol == "udp" {
					log.Println("listening (udp) on:", addr)
					lis, err := kcp.Listen(addr)
					checkError(err)
					wg.Add(1)
					go loop(lis)
				} else {
					log.Printf("Protocol %s is not supported on %s", protocol, addr)
				}
			}

		} else {

			// udp stack
			if config.ListenUDP != "" {
				log.Println("listening (udp) on:", config.ListenUDP)
				lis, err := kcp.Listen(config.ListenUDP)
				checkError(err)
				wg.Add(1)
				go loop(lis)
			}

			// tcp stack
			if config.ListenTCP != "" {
				if conn, err := tcpraw.Listen("tcp", config.ListenTCP); err == nil {
					log.Println("listening (tcp) on:", config.ListenTCP)
					lis, err := kcp.ServeConn(conn)
					checkError(err)
					wg.Add(1)
					go loop(lis)
				} else {
					log.Println(err)
				}
			}
		}

		wg.Wait()
		return nil
	}

	myApp.Run(os.Args)
}
