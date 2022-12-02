package main

import (
	"bytes"
	"context"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/json"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"tunnel3MeServer/tunnels"
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

	// web app
	h := server.Default(
		server.WithHostPorts("127.0.0.1:" + serverPortArg),
	)

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

	h.GET("/create-host/:id", func(c context.Context, ctx *app.RequestContext) {
		tunnelId := ctx.Param("id")
		hlog.Info("Create host: ", tunnelId)

		if tunnelId == "config" {
			ctx.AbortWithMsg("Disallowed to create this host", 401)
			return
		}

		err, newPort, newWebsocketPort := registerHost(tunnelId, baseDomain, &nextReversePort, &nextWebsocketPort)
		if err != nil {
			hlog.Warn(err)
			ctx.JSON(consts.StatusBadGateway, utils.H{"error": err})
			return
		}

		go tunnels.OpenNewServerHandler(newPort, newWebsocketPort)

		ctx.JSON(consts.StatusOK, utils.H{"result": newWebsocketPort})
	})

	h.GET("/delete-host/:id", func(c context.Context, ctx *app.RequestContext) {
		hostId := ctx.Param("id")
		hlog.Info("Delete host: ", hostId)

		if hostId == "config" {
			ctx.AbortWithMsg("Disallowed to delete this host", 401)
			return
		}

		res := deleteHost(hostId)
		ctx.JSON(consts.StatusOK, utils.H{"result": res})
	})

	h.Spin()
}

func registerHost(tunnelId string, baseDomain string, nextReversePort *uint64, nextWebsocketPort *uint64) (err error, newPort string, newWebsocketPort string) {
	host := tunnelId + baseDomain
	deleteHost(tunnelId)

	newPort = strconv.FormatUint(*nextReversePort, 10)
	atomic.AddUint64(nextReversePort, 1)

	newWebsocketPort = strconv.FormatUint(*nextWebsocketPort, 10)
	atomic.AddUint64(nextWebsocketPort, 1)

	values := `{
			"@id": "` + tunnelId + `",
			"match": [{
				"host": ["` + host + `"]
			}],
			"handle": [{
				"handler": "reverse_proxy",
				"upstreams":[{
					"dial": ":` + newPort + `"
				}]
			}]
		}`

	_, err = http.Post("http://127.0.0.1:2019/config/apps/http/servers/srv0/routes", "application/json", bytes.NewBuffer([]byte(values)))
	return err, "", ""
}

func deleteHost(hostId string) map[string]interface{} {
	req, err := http.NewRequest("DELETE", "http://127.0.0.1:2019/id/"+hostId, nil)
	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		hlog.Warn(err)
		return nil
	}

	var res map[string]interface{}

	json.NewDecoder(resp.Body).Decode(&res)

	return res
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
