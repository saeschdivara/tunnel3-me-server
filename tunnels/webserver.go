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

type TunnelingServer struct {
	requestChannel           chan RequestInfo
	responseChannel          chan ResponseInfo
	serverUnavailableChannel chan bool
}

func MakeTunnelingServer() *TunnelingServer {
	srv := &TunnelingServer{
		requestChannel:           make(chan RequestInfo),
		responseChannel:          make(chan ResponseInfo),
		serverUnavailableChannel: make(chan bool),
	}

	return srv
}

func (srv *TunnelingServer) TunnelRequest(ctx *app.RequestContext) {
	method := string(ctx.Method())
	uri := ctx.URI()

	t1 := time.Now()
	hlog.Info("Request > ", method, "[", uri.String(), "]")

	body, _ := ctx.Body()

	srv.requestChannel <- RequestInfo{
		Method:  method,
		Body:    string(body),
		Headers: ctx.GetRequest().Header.RawHeaders(),
		Path:    string(uri.RequestURI()),
	}

	select {
	case responseData := <-srv.responseChannel:
		t2 := time.Now()

		ctx.Response.SetStatusCode(responseData.StatusCode)

		hlog.Info("Response > ", method, "[", uri.String(), "] ", responseData.StatusCode, " - ", t2.Sub(t1).String())

		for key, values := range responseData.Headers {
			for _, value := range values {
				ctx.Response.Header.Add(key, value)
			}
		}
		ctx.Response.SetBodyRaw([]byte(responseData.Body))

	case available := <-srv.serverUnavailableChannel:
		if !available {
			t2 := time.Now()

			hlog.Info("Unavailable > ", method, "[", uri.String(), "] 500 - ", t2.Sub(t1).String())

			ctx.Response.SetStatusCode(502)
			ctx.Header("Content-Type", "text/html; charset=utf-8")
			ctx.Response.SetBodyRaw([]byte("<html><body>Server unavailable</body></html>"))
		}
	}
}

func (srv *TunnelingServer) ForwardRequest(conn *websocket.Conn, port string) {
	request := <-srv.requestChannel
	hlog.Info("Receive request <", port, ">")

	err := conn.WriteMessage(websocket.TextMessage, []byte(request.ToString()))
	if err != nil {
		srv.serverUnavailableChannel <- false
		return
	}

	hlog.Info("Message sent <", port, ">")

	_, responseMessage, err := conn.ReadMessage()
	if err != nil {
		srv.serverUnavailableChannel <- false
		return
	}

	hlog.Info("Message received <", port, ">")

	response := ResponseInfo{}
	json.Unmarshal(responseMessage, &response)

	srv.responseChannel <- response
	hlog.Info("Response received <", port, ">")
}

func OpenNewServerHandler(port string, websocketPort string) {
	h := server.Default(
		server.WithHostPorts("127.0.0.1:" + port),
	)

	srv := MakeTunnelingServer()

	h.Any("/*path", func(c context.Context, ctx *app.RequestContext) {
		userAgent := string(ctx.Request.Header.UserAgent())

		if userAgent == "python-requests/2.22.0" {
			ctx.AbortWithMsg("Ok", 200)
			return
		}

		k := func(conn *websocket.Conn) {}

		if err := upgrader.Upgrade(ctx, k); err != nil {
			srv.TunnelRequest(ctx)
		}
	})

	go openWebSocketServer(srv, websocketPort)

	h.Spin()
}

func openWebSocketServer(srv *TunnelingServer, port string) {
	h := server.Default(
		server.WithHostPorts("0.0.0.0:" + port),
	)

	// TODO: handle no client element connected when receiving requests

	h.GET("/", func(c context.Context, ctx *app.RequestContext) {
		err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
			for {
				srv.ForwardRequest(conn, port)
			}
		})

		if err != nil {
			hlog.Warn("upgrade:", err)
			return
		}
	})

	h.Spin()
}
