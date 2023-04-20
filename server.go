package main

import (
	"bytes"
	"context"
	"github.com/cloudwego/hertz/pkg/common/json"
	"github.com/getsentry/sentry-go"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/hertz-contrib/websocket"
)

var upgrader = websocket.HertzUpgrader{} // use default options

func main() {
	argsWithoutProg := os.Args[1:]

	if len(argsWithoutProg) != 4 {
		hlog.Fatal("Missing parameters [base-domain] [server-port] [web-app-port-start] [web-socket-port-start]")
	}

	baseDomain := argsWithoutProg[0] // example: .tunnel3.me

	err := sentry.Init(sentry.ClientOptions{
		Dsn: "https://6962e24b976c45e8b2ebcce01cd23095@o372537.ingest.sentry.io/4505045778694144",
		// Enable printing of SDK debug messages.
		// Useful when getting started or trying to figure something out.
		Debug: true,

		// performance monitoring: https://docs.sentry.io/platforms/go/performance/
		EnableTracing: true,
		// Specify a fixed sample rate:
		// We recommend adjusting this value in production
		TracesSampleRate: 1.0,
	})
	if err != nil {
		log.Fatalf("sentry.Init: %s", err)
	}
	// Flush buffered events before the program terminates.
	// Set the timeout to the maximum duration the program can afford to wait.
	defer sentry.Flush(2 * time.Second)

	// setup logging file
	httpLogFile := setupLoggerWithFileOutput("http.log", hlog.SystemLogger())
	defer httpLogFile.Close()

	appLogFile := setupLoggerWithFileOutput("app.log", hlog.DefaultLogger())
	defer appLogFile.Close()

	serverPortArg := argsWithoutProg[1]
	_, err = strconv.Atoi(serverPortArg)

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

		host := tunnelId + baseDomain

		deleteHost(tunnelId)

		newPort := strconv.FormatUint(nextReversePort, 10)
		atomic.AddUint64(&nextReversePort, 1)

		newWebsocketPort := strconv.FormatUint(nextWebsocketPort, 10)
		atomic.AddUint64(&nextWebsocketPort, 1)

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

		resp, err := http.Post("http://127.0.0.1:2019/config/apps/http/servers/srv0/routes", "application/json", bytes.NewBuffer([]byte(values)))

		if err != nil {
			hlog.Warn(err)
			ctx.JSON(consts.StatusBadGateway, utils.H{"error": err})
			return
		}

		var res map[string]interface{}

		json.NewDecoder(resp.Body).Decode(&res)

		go openNewServerHandler(newPort, newWebsocketPort)

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

type RequestInfo struct {
	Method  string
	Body    string
	Headers []byte
	Path    string
}

type ResponseInfo struct {
	Body       string
	Headers    map[string][]string
	StatusCode int
}

func (info RequestInfo) ToString() string {
	jsonData, err := json.Marshal(info)

	if err != nil {
		return "<error>"
	}

	return string(jsonData)
}

func openNewServerHandler(port string, websocketPort string) {
	h := server.Default(
		server.WithHostPorts("127.0.0.1:" + port),
	)

	requestChannel := make(chan RequestInfo)
	responseChannel := make(chan ResponseInfo)
	serverUnavailableChannel := make(chan bool)

	h.Any("/*path", func(c context.Context, ctx *app.RequestContext) {
		userAgent := string(ctx.Request.Header.UserAgent())

		if userAgent == "python-requests/2.22.0" {
			ctx.AbortWithMsg("Ok", 200)
			return
		}

		method := string(ctx.Method())
		uri := ctx.URI()

		t1 := time.Now()
		hlog.Info("Request > ", method, "[", uri.String(), "]")

		body, _ := ctx.Body()

		requestChannel <- RequestInfo{
			Method:  method,
			Body:    string(body),
			Headers: ctx.GetRequest().Header.RawHeaders(),
			Path:    string(uri.RequestURI()),
		}

		select {
		case responseData := <-responseChannel:
			t2 := time.Now()

			ctx.Response.SetStatusCode(responseData.StatusCode)

			hlog.Info("Response > ", method, "[", uri.String(), "] ", responseData.StatusCode, " - ", t2.Sub(t1).String())

			for key, values := range responseData.Headers {
				for _, value := range values {
					ctx.Response.Header.Add(key, value)
				}
			}
			ctx.Response.SetBodyRaw([]byte(responseData.Body))

		case available := <-serverUnavailableChannel:
			if !available {
				t2 := time.Now()

				hlog.Info("Unavailable > ", method, "[", uri.String(), "] 500 - ", t2.Sub(t1).String())

				ctx.Response.SetStatusCode(502)
				ctx.Header("Content-Type", "text/html; charset=utf-8")
				ctx.Response.SetBodyRaw([]byte("<html><body>Server unavailable</body></html>"))
			}
		}
	})

	go openWebSocketServer(requestChannel, responseChannel, serverUnavailableChannel, websocketPort)

	h.Spin()
}

func openWebSocketServer(requestChannel <-chan RequestInfo, responseChannel chan<- ResponseInfo, serverUnavailableChannel chan<- bool, port string) {
	h := server.Default(
		server.WithHostPorts("0.0.0.0:" + port),
	)

	// TODO: handle no client element connected when receiving requests

	h.GET("/", func(c context.Context, ctx *app.RequestContext) {
		err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
			for {
				request := <-requestChannel
				hlog.Info("Receive request <", port, ">")

				err := conn.WriteMessage(websocket.TextMessage, []byte(request.ToString()))
				if err != nil {
					serverUnavailableChannel <- false
					continue
				}

				hlog.Info("Message sent <", port, ">")

				_, responseMessage, err := conn.ReadMessage()
				if err != nil {
					serverUnavailableChannel <- false
					continue
				}

				hlog.Info("Message received <", port, ">")

				response := ResponseInfo{}
				json.Unmarshal(responseMessage, &response)

				responseChannel <- response

				hlog.Info("Response received <", port, ">")
			}
		})

		if err != nil {
			hlog.Warn("upgrade:", err)
			return
		}
	})

	h.Spin()
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
