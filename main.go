package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
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
			s = strings.Replace(s, "\n", "", -1)
			s = strings.Replace(s, "\r", "", -1)
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
	sendMsg := func(topic string, msg []byte) {
		t := c.Publish(topic, 0, false, msg)
		if !t.WaitTimeout(3 * time.Second) {
			log.Println("Action not completed")
			if err := t.Error(); err != nil {
				log.Println("Error: ", err)
			}
		}
	}
	go func() {
		for recv := range recvChan {
			if len(recv) > 1 && recv[0] == '>' {
				data, err := parseReceived(recv[1:])
				if err != nil {
					log.Println("Error: ", err)
					sendMsg("gateway/rfm69", []byte(recv))
					continue
				}
				b, err := json.Marshal(data)
				if err != nil {
					log.Println("Error: ", err)
					sendMsg("gateway/rfm69/recv", []byte(recv))
					continue
				}
				sendMsg("gateway/rfm69/recv/"+strconv.Itoa(data.Sender), b)
			} else {
				sendMsg("gateway/rfm69", []byte(recv))
			}
		}
	}()
}

type recvData struct {
	Sender       int                    `json:"sender"`
	RSSI         int                    `json:"rssi"`
	AckRequested bool                   `json:"ack_req"`
	Message      map[string]interface{} `json:"msg"`
}

func parseReceived(s string) (*recvData, error) {
	if len(s) == 0 {
		return nil, fmt.Errorf("Invalid input string")
	}
	metaNmsg := strings.SplitN(s, "|", 2)
	if len(metaNmsg) != 2 {
		return nil, fmt.Errorf("Could not split meta and message")
	}
	metaParts := strings.Split(metaNmsg[0], ",")
	meta := &recvData{
		Message: make(map[string]interface{}),
	}
	for _, metaPart := range metaParts {
		parts := strings.SplitN(metaPart, ":", 2)
		switch parts[0] {
		case "s":
			meta.Sender, _ = strconv.Atoi(parts[1])
		case "rssi":
			meta.RSSI, _ = strconv.Atoi(parts[1])
		case "ackreq":
			if parts[1] == "1" {
				meta.AckRequested = true
			}
		}
	}
	if metaNmsg[1][0] == '!' {
		meta.Message["register"] = true
	} else {
		msgParts := strings.Split(metaNmsg[1], "|")
		for _, msgPart := range msgParts {
			keyValue := strings.SplitN(msgPart, ":", 2)
			if n, err := strconv.Atoi(keyValue[1]); err == nil {
				meta.Message[keyValue[0]] = n
			} else {
				meta.Message[keyValue[0]] = keyValue[1]
			}
		}
	}
	return meta, nil
}
