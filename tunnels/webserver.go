package tunnels

import (
	"context"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/json"
	"github.com/hertz-contrib/websocket"
	"time"
)

var upgrader = websocket.HertzUpgrader{} // use default options

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

func OpenNewServerHandler(port string, websocketPort string) {
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
