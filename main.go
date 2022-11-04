package main

import (
	"io"
	"log"
	"net"
	"time"
)

const (
	HOST       = "localhost"
	TYPE       = "tcp"
	ReadMax    = 4096 * 2
	TimeToWait = 10
)

func doHttpProxyThing(port string, proxyPort string) {
	listen, err := net.Listen(TYPE, HOST+":"+port)
	if err != nil {
		log.Fatal(err)
	}
	// close listener
	defer listen.Close()
	for {
		conn, err := listen.Accept()

		if err != nil {
			log.Fatal(err)
		}

		go handleIncomingRequest(conn, proxyPort)
	}
}

func main() {
	go func() {
		doHttpProxyThing("9000", "8080")
	}()

	doHttpProxyThing("9001", "8083")
}

func handleIncomingRequest(conn net.Conn, proxyPort string) {

	proxiedCon, errProxyCon := net.Dial("tcp", "localhost:"+proxyPort)
	if errProxyCon != nil {
		log.Fatal(errProxyCon)
	}

	// setup clean-ups
	defer conn.Close()
	defer proxiedCon.Close()

	// store incoming data
	bufferIn := make([]byte, ReadMax)

	// store incoming data
	bufferOut := make([]byte, ReadMax)

	for {
		conn.SetReadDeadline(time.Now().Add(time.Millisecond * TimeToWait))

		_, err := conn.Read(bufferIn)

		if err != nil {
			if err == io.EOF {
				break
			}
		}

		if err == nil {
			proxiedCon.Write(bufferIn)
		}

		proxiedCon.SetReadDeadline(time.Now().Add(time.Millisecond * TimeToWait))
		_, err = proxiedCon.Read(bufferOut)
		//if err != nil {
		//	break
		//}

		if err == nil {
			conn.Write(bufferOut)
		}
	}
}
