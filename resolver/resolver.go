// Copyright 2009 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// DNS resolver client: see RFC 1035.
// A dns resolver is to be run as a seperate goroutine. 
// For every reply the resolver answers by sending the
// received packet (with a possible error) back on the channel.
// 
// Basic usage pattern for setting up a resolver:
//
//        res := new(Resolver)
//        ch := NewQuerier(res)               // start new resolver
//        res.Servers = []string{"127.0.0.1"} // set the nameserver
//
//        m := new(Msg)                       // prepare a new message
//        m.MsgHdr.Recursion_desired = true   // header bits
//        m.Question = make([]Question, 1)    // 1 RR in question sec.
//        m.Question[0] = Question{"miek.nl", TypeSOA, ClassINET}
//        ch <- DnsMsg{m, nil}                // send the query
//        in := <-ch                          // wait for reply
//
package resolver

import (
	"os"
	"rand"
	"time"
	"net"
        "dns"
)

// When communicating with a resolver, we use this structure
// to send packets to it, for sending Error must be nil.
// A resolver responds with a reply packet and a possible error.
// Sending a nil message instructs to resolver to stop.
type DnsMsg struct {
	Dns   *dns.Msg
	Error os.Error
}

type Resolver struct {
	Servers  []string            // servers to use
	Search   []string            // suffixes to append to local name
	Port     string              // what port to use
	Ndots    int                 // number of dots in name to trigger absolute lookup
	Timeout  int                 // seconds before giving up on packet
	Attempts int                 // lost packets before giving up on server
	Rotate   bool                // round robin among servers
	Tcp      bool                // use TCP
	Mangle   func([]byte) []byte // Mangle the packet
}

// Start a new resolver as a goroutine, return the communication channel
func NewQuerier(res *Resolver) (ch chan DnsMsg) {
	ch = make(chan DnsMsg)
	go query(res, ch)
	return
}

// Start a new xfr as a goroutine, return a channel.
// Channel will be closed when the axfr is finished, until
// that time new messages will appear on the channel
func NewXfer(res *Resolver) (ch chan DnsMsg) {

}

// The query function.
func query(res *Resolver, msg chan DnsMsg) {
	// TODO port number, error checking, robustness
	var c net.Conn
	var err os.Error
	var in *dns.Msg
        var port string
        // len(res.Server) == 0 can be perfectly valid, when setting up the resolver
        if res.Port == "" {
                port = "53"
        } else {
                port = res.Port
        }

	for {
		select {
		case out := <-msg: //msg received
			if out.Dns == nil {
				// nil message, quit the goroutine
				msg <- DnsMsg{nil, nil}
				close(msg)
				return
			}

			var cerr os.Error
			// Set an id
			//if len(name) >= 256 {
			out.Dns.Id = uint16(rand.Int()) ^ uint16(time.Nanoseconds())
			sending, ok := out.Dns.Pack()
			if !ok {
                                //println("pack failed")
				msg <- DnsMsg{nil, nil} // todo error
                                continue;
			}

			for i := 0; i < len(res.Servers); i++ {
				server := res.Servers[i] + ":" + port
				if res.Tcp {
					c, cerr = net.Dial("tcp", "", server)
				} else {
					c, cerr = net.Dial("udp", "", server)
				}
				if cerr != nil {
					err = cerr
					continue
				}
                                if res.Tcp {
                                        in, err = exchange_tcp(c, sending, res)
                                } else {
                                        in, err = exchange_udp(c, sending, res)
                                }

				// Check id in.id != out.id
                                // TODO(mg)

				c.Close()
				if err != nil {
					continue
				}
			}
			if err != nil {
				msg <- DnsMsg{nil, err}
			} else {
				msg <- DnsMsg{in, nil}
			}
		}
	}
	return
}

// Send a request on the connection and hope for a reply.
// Up to res.Attempts attempts.
func exchange_udp(c net.Conn, m []byte, r *Resolver) (*dns.Msg, os.Error) {
        var timeout int64
        var attempts int
	if r.Mangle != nil {
		m = r.Mangle(m)
	}
        if r.Timeout == 0 {
                timeout = 1
        } else {
                timeout = int64(r.Timeout)
        }
        if r.Attempts == 0 {
                attempts = 1
        } else {
                attempts = r.Attempts
        }
	for a:= 0; a < attempts; a++ {
		n, err := c.Write(m)
		if err != nil {
                        //println("error writing")
			return nil, err
		}

		c.SetReadTimeout(timeout * 1e9)         // nanoseconds
		buf := make([]byte, dns.DefaultMsgSize) // More than enough???
		n, err = c.Read(buf)
		if err != nil {
                        //println("error reading")
                        //println(err.String())
			// More Go foo needed
			//if e, ok := err.(Error); ok && e.Timeout() {
			//	continue
			//}
			return nil, err
		}
		buf = buf[0:n]
		in := new(dns.Msg)
		if !in.Unpack(buf) {
			continue
		}
		return in, nil
	}
	return nil, nil // todo error
}

// Up to res.Attempts attempts.
func exchange_tcp(c net.Conn, m []byte, r *Resolver) (*dns.Msg, os.Error) {
        var timeout int64
        var attempts int
        if r.Mangle != nil {
                m = r.Mangle(m)
        }
        if r.Timeout == 0 {
                timeout = 1
        } else {
                timeout = int64(r.Timeout)
        }
        if r.Attempts == 0 {
                attempts = 1
        } else {
                attempts = r.Attempts
        }

        ls := make([]byte, 2) // sender length
        lr := make([]byte, 2) // receiver length
        var length uint16
        ls[0] = byte(len(m) >> 8)
        ls[1] = byte(len(m))
        for a := 0; a < attempts; a++ {
                // With DNS over TCP we first send the length
                _, err := c.Write(ls)
                if err != nil {
                        return nil, err
                }

                // And then send the message
                _, err = c.Write(m)
                if err != nil {
                        return nil, err
                }

                c.SetReadTimeout(timeout * 1e9) // nanoseconds
                // The server replies with two bytes length
                _, err = c.Read(lr)
                if err != nil {
                        return nil, err
                }
                length = uint16(lr[0])<<8 | uint16(lr[1])
                // if length is 0??
                // And then the message
                buf := make([]byte, length)
                _, err = c.Read(buf)
                if err != nil {
                        //println("error reading")
                        //println(err.String())
                        // More Go foo needed
                        //if e, ok := err.(Error); ok && e.Timeout() {
                        //      continue
                        //} 
                        return nil, err
                }
                in := new(dns.Msg)
                if !in.Unpack(buf) {
//                        println("unpacking went wrong")
                        continue
                }
                return in, nil
        }
        return nil, nil // todo error
}
