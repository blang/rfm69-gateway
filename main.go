package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/tarm/serial"
)

func scanBeginSerial(baud int) (*serial.Port, error) {
	for i := 0; i < 5; i++ {
		c := &serial.Config{Name: "/dev/ttyUSB" + strconv.Itoa(i), Baud: baud}
		s, err := serial.OpenPort(c)
		if err == nil {
			log.Println("Opened serial on port: ", i)
			return s, nil
		}
	}
	return nil, errors.New("No serial port found")
}

func handleSerial(s *serial.Port, recvChan chan string, sendChan chan string) {
	wg := &sync.WaitGroup{}
	wg.Add(2)
	go func(p *serial.Port) {
		br := bufio.NewReader(s)
		for {
			s, err := br.ReadString('\n')
			if err != nil {
				if err == io.EOF {
					log.Println("Serial connection closed")
					os.Exit(1)
				}
				log.Println("Error:", err)
			}
			log.Printf("%q", s)
			recvChan <- s
		}
		// wg.Done() // TODO: unreachable
	}(s)

	go func(p *serial.Port) {
		bw := bufio.NewWriter(p)
		for str := range sendChan {
			log.Println("Received new send request:", str)
			_, err := bw.WriteString(str + "\n")
			if err != nil {
				log.Println("Error sending: ", err)
			}
			err = bw.Flush()
			p.Flush()
			if err != nil {
				log.Println("Error flushing buffer: ", err)
			}
		}
		wg.Done()
	}(s)
	wg.Wait()
}

func main() {
	recvChan := make(chan string)
	sendChan := make(chan string)
	p, err := scanBeginSerial(115200)
	if err != nil {
		log.Println("Error: ", err)
		os.Exit(1)
	}
	go handleSerial(p, recvChan, sendChan)

	mqtt.ERROR = log.New(os.Stderr, "ERR: ", log.Llongfile)
	mqtt.CRITICAL = log.New(os.Stderr, "CRIT: ", log.Llongfile)
	opt := mqtt.NewClientOptions()
	opt.AddBroker("tcp://127.0.0.1:1883")
	opt.SetAutoReconnect(true)
	opt.SetKeepAlive(3 * time.Second)
	opt.SetClientID("RFM69 Gateway")
	opt.SetConnectionLostHandler(func(c mqtt.Client, err error) {
		log.Println("Connection lost")
	})
	opt.SetOnConnectHandler(mqtt.OnConnectHandler(func(c mqtt.Client) {
		fmt.Println("Connected")
		c.Publish("test", 0, false, "Connected")
		handleConnected(recvChan, sendChan, c)
	}))
	cli := mqtt.NewClient(opt)
	token := cli.Connect()
	token.WaitTimeout(5 * time.Second)

	if err := token.Error(); err != nil {
		log.Println("Error: ", err)
		os.Exit(1)
	}
	wg := &sync.WaitGroup{}
	wg.Add(1)
	wg.Wait()
}

func handleConnected(recvChan chan string, sendChan chan string, c mqtt.Client) {
	c.Subscribe("gateway/rfm69/send", 0, mqtt.MessageHandler(func(c mqtt.Client, m mqtt.Message) {
		sendChan <- string(m.Payload())
	}))
	go func() {
		for recv := range recvChan {
			t := c.Publish("gateway/rfm69/recv", 0, false, []byte(recv))
			if !t.WaitTimeout(3 * time.Second) {
				log.Println("Action not completed")
				if err := t.Error(); err != nil {
					log.Println("Error: ", err)
				}
			}
		}
	}()
}
