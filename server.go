package main

import (
	"io"
	"log"
	"os"
	"strconv"
	"tunnel3MeServer/tunneling"

	"github.com/cloudwego/hertz/pkg/common/hlog"
)

func main() {
	argsWithoutProg := os.Args[1:]

	if len(argsWithoutProg) != 4 {
		hlog.Fatal("Missing parameters [base-domain] [server-port] [web-app-port-start] [web-socket-port-start]")
	}

	baseDomain := argsWithoutProg[0] // example: .tunnel3.me

	// setup logging file
	httpLogFile := setupLoggerWithFileOutput("http.log", hlog.SystemLogger())
	defer httpLogFile.Close()

	appLogFile := setupLoggerWithFileOutput("app.log", hlog.DefaultLogger())
	defer appLogFile.Close()

	serverPortArg := argsWithoutProg[1]
	_, err := strconv.Atoi(serverPortArg)

	if err != nil {
		hlog.Fatal("Could not convert server-port (", serverPortArg, ") to int")
	}

	webAppPortArg := argsWithoutProg[2]
	var nextReversePort uint64
	nextReversePort, err = strconv.ParseUint(webAppPortArg, 10, 32)

	if err != nil {
		hlog.Fatal("Could not convert web-app-port (", webAppPortArg, ") to int")
	}

	webSocketPortArg := argsWithoutProg[3]
	var nextWebsocketPort uint64
	nextWebsocketPort, err = strconv.ParseUint(webSocketPortArg, 10, 32)

	if err != nil {
		hlog.Fatal("Could not convert web-socket-port (", webSocketPortArg, ") to int")
	}

	tunneling.SetupTunnelingInfrastructure(baseDomain, serverPortArg, nextReversePort, nextWebsocketPort)
}

func setupLoggerWithFileOutput(logFilename string, logger hlog.FullLogger) *os.File {
	loggingFile, err := os.OpenFile(logFilename, os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening %s file: %v", logFilename, err)
	}

	wrt := io.MultiWriter(os.Stdout, loggingFile)
	logger.SetOutput(wrt) // default logger

	return loggingFile
}
