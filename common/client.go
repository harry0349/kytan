package common

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/changlan/mangi/tun"
	"log"
	"net"
	"strconv"
	"os"
	"os/signal"
	"syscall"
	"github.com/changlan/mangi/util"
	"fmt"
	"github.com/changlan/mangi/crypto"
)

type Client struct {
	tun  *tun.TunDevice
	conn *net.UDPConn
	addr *net.UDPAddr
	gw string
	key []byte
}

func NewClient(server_name string, port int, key []byte) (*Client, error) {
	addr, err := net.ResolveUDPAddr("udp", server_name+":"+strconv.Itoa(port))
	if err != nil {
		return nil, err
	}

	log.Printf("Connecting to %s over UDP.", addr.String())
	conn, err := net.DialUDP("udp", nil, addr)

	return &Client{
		nil,
		conn,
		addr,
		"",
		key,
	}, nil
}

func (c *Client) handleTun(err_chan chan error) {
	defer c.tun.Close()
	for {
		pkt, err := c.tun.Read()

		log.Printf("%s -> %s", c.tun.String(), c.conn.RemoteAddr().String())

		if err != nil {
			err_chan <- err
			return
		}
		buffer := new(bytes.Buffer)

		err = binary.Write(buffer, binary.BigEndian, Magic)
		if err != nil {
			err_chan <- err
			return
		}

		err = binary.Write(buffer, binary.BigEndian, Data)
		if err != nil {
			err_chan <- err
			return
		}

		_, err = buffer.Write(pkt)
		if err != nil {
			err_chan <- err
			return
		}

		data, err := crypto.Encrypt(c.key, buffer.Bytes())
		if err != nil {
			err_chan <- err
			return
		}

		_, err = c.conn.Write(data)

		if err != nil {
			err_chan <- err
			return
		}
	}
}

func (c *Client) handleUDP(err_chan chan error) {
	defer c.conn.Close()
	for {
		buf := make([]byte, 1600)
		n, err := c.conn.Read(buf)

		log.Printf("%s -> %s", c.conn.RemoteAddr().String(), c.tun.String())

		if err != nil {
			err_chan <- err
			return
		}
		if n < 5 {
			err = errors.New("Malformed UDP packet. Length less than 5.")
			err_chan <- err
			return
		}

		buf, err = crypto.Decrypt(c.key, buf)
		if err != nil {
			err_chan <- err
			return
		}

		reader := bytes.NewReader(buf)
		var magic uint32
		err = binary.Read(reader, binary.BigEndian, &magic)

		if err != nil {
			err_chan <- err
			return
		}

		if magic != Magic {
			err = errors.New("Malformed UDP packet. Invalid MAGIC.")
			err_chan <- err
			return
		}

		var message_type uint8
		err = binary.Read(reader, binary.BigEndian, &message_type)

		if err != nil {
			err_chan <- err
			return
		}

		if message_type != Data {
			err = errors.New("Unexpected message type.")
			err_chan <- err
			return
		}

		pkt := buf[5:n]
		err = c.tun.Write(pkt)
		if err != nil {
			err_chan <- err
			return
		}
	}
}

func (c *Client) init() error {
	buffer := new(bytes.Buffer)
	err := binary.Write(buffer, binary.BigEndian, Magic)
	if err != nil {
		return err
	}

	err = binary.Write(buffer, binary.BigEndian, Request)
	if err != nil {
		return err
	}

	log.Printf("Sending request to %s.", c.conn.RemoteAddr().String())

	data, err := crypto.Encrypt(c.key, buffer.Bytes())
	if err != nil {
		return err
	}

	_, err = c.conn.Write(data)
	if err != nil {
		return err
	}

	buf := make([]byte, 1600)
	n, err := c.conn.Read(buf)
	if err != nil {
		return err
	}
	log.Printf("Response received.")
	if n != 4 + 1 + 4 {
		return errors.New("Incorrect acceptance.")
	}

	buf, err = crypto.Decrypt(c.key, buf)
	if err != nil {
		return err
	}

	reader := bytes.NewReader(buf)

	var magic uint32
	var message_type uint8

	err = binary.Read(reader, binary.BigEndian, &magic)
	if err != nil {
		return err
	}

	err = binary.Read(reader, binary.BigEndian, &message_type)
	if err != nil {
		return err
	}

	if magic != Magic {
		return errors.New("Malformed UDP packet. Invalid MAGIC.")
	}

	if message_type != Accept {
		return errors.New("Unexpected message type.")
	}

	var local_ip net.IP
	local_ip = buf[5:n]

	log.Printf("Client IP %s assigned.", local_ip.String())
	c.tun, err = tun.NewTun("tun0", local_ip.String())
	if err != nil {
		return err
	}

	local_ip[3] = 0x1
	c.gw, err = util.DefaultGateway()
	if err != nil {
		return err
	}
	err = util.SetGatewayForHost(c.gw, c.addr.IP.String())
	if err != nil {
		return err
	}
	err = util.ClearGateway()
	if err != nil {
		return err
	}
	err = util.SetDefaultGateway(local_ip.String())
	if err != nil {
		return err
	}

	return nil
}

func (c *Client) Run() {
	err := c.init()

	if err != nil {
		log.Fatal(err)
	}

	err_chan := make(chan error)

	go c.handleTun(err_chan)
	go c.handleUDP(err_chan)
	go c.handleSignal(err_chan)

	err = <- err_chan
	log.Print(err)

	c.cleanup()
}

func (c *Client) cleanup() {
	c.tun.Close()
	c.conn.Close()

	util.ClearGateway()
	util.SetDefaultGateway(c.gw)
	util.ClearGatewayForHost(c.addr.IP.String())
}

func (c *Client) handleSignal(err_chan chan error) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigs

	msg := fmt.Sprintf("%s received.", sig.String())
	log.Printf(msg)

	err_chan <- errors.New(msg)
}