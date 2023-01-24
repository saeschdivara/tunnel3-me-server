package tunnels

import (
	"context"
	"fmt"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/json"
	"github.com/hertz-contrib/websocket"
	"github.com/savsgio/gotils/strconv"
	"strings"
	"time"
)

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

type ErrorResponse struct {
	Message string
}

type MessageInfo struct {
	Path        string
	MessageType int
	Data        []byte
}

type TunnelingServer struct {
	requestChannel           chan RequestInfo
	responseChannel          chan ResponseInfo
	serverUnavailableChannel chan bool
	messageReceiveChannel    chan MessageInfo
	messageSendChannel       chan MessageInfo
}

func MakeTunnelingServer() *TunnelingServer {
	srv := &TunnelingServer{
		requestChannel:           make(chan RequestInfo),
		responseChannel:          make(chan ResponseInfo),
		messageReceiveChannel:    make(chan MessageInfo),
		messageSendChannel:       make(chan MessageInfo),
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

func (srv *TunnelingServer) TunnelWebsocket(ctx *app.RequestContext, upgrader *websocket.HertzUpgrader) {
	uri := ctx.URI()
	hlog.Info("Open Tunnel > ", "[", uri.String(), "]")

	err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {

		// send control message to open tunnel
		conn.WriteMessage(websocket.TextMessage, []byte(""))

		for {

			messageType, data, err := conn.ReadMessage()
			if err == nil {
				srv.messageReceiveChannel <- MessageInfo{
					Path:        string(uri.RequestURI()),
					MessageType: messageType,
					Data:        data,
				}

				responseData := <-srv.messageSendChannel
				conn.WriteMessage(responseData.MessageType, responseData.Data)
			} else {
				_, correct := err.(*websocket.CloseError)
				if correct {
					hlog.Info("Closing message detected")
					break
				}
			}
		}
	})

	hlog.Warn("Could not establish websocket connection: ", err)
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

func OpenNewServerHandler(domain string, port string, websocketPort string) {
	h := server.Default(
		server.WithHostPorts("127.0.0.1:" + port),
	)

	srv := MakeTunnelingServer()

	var upgrader = &websocket.HertzUpgrader{
		CheckOrigin: func(ctx *app.RequestContext) bool {
			host := strconv.B2S(ctx.Host())
			isNormalHost := strings.HasSuffix(host, fmt.Sprint(domain))
			isControlHost := strings.HasSuffix(host, fmt.Sprint(domain, ":", websocketPort))
			return isNormalHost || isControlHost
		},
	}

	h.Any("/*path", func(c context.Context, ctx *app.RequestContext) {
		userAgent := string(ctx.Request.Header.UserAgent())

		if userAgent == "python-requests/2.22.0" {
			ctx.AbortWithMsg("Ok", 200)
			return
		}

		if strings.Contains(strconv.B2S(ctx.Request.Header.Peek("Connection")), "Upgrade") {
			srv.TunnelWebsocket(ctx, upgrader)
		} else {
			srv.TunnelRequest(ctx)
		}
	})

	go openWebSocketServer(srv, upgrader, websocketPort)

	h.Spin()
}

func openWebSocketServer(srv *TunnelingServer, upgrader *websocket.HertzUpgrader, port string) {
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
