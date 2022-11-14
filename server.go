package main

import (
	"bytes"
	"context"
	"github.com/cloudwego/hertz/pkg/common/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/common/utils"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/hertz-contrib/websocket"
)

var upgrader = websocket.HertzUpgrader{} // use default options

func main() {
	// setup logging file
	f, err := os.OpenFile("app.log", os.O_RDWR|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		log.Fatalf("error opening file: %v", err)
	}
	defer f.Close()

	wrt := io.MultiWriter(os.Stdout, f)

	log.SetOutput(wrt)
	hlog.DefaultLogger().SetOutput(wrt)
	hlog.SystemLogger().SetOutput(wrt)

	// web app
	h := server.Default()

	nextReversePort := 5000
	nextWebsocketPort := 10000

	h.GET("/create-host/:id", func(c context.Context, ctx *app.RequestContext) {
		tunnelId := ctx.Param("id")

		if tunnelId == "config" {
			ctx.AbortWithMsg("Disallowed to create this host", 401)
			return
		}

		host := tunnelId + ".tunnel3.me"

		deleteHost(tunnelId)

		newPort := strconv.Itoa(nextReversePort)
		nextReversePort += 1

		newWebsocketPort := strconv.Itoa(nextWebsocketPort)
		nextWebsocketPort += 1

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
			log.Fatal(err)
		}

		var res map[string]interface{}

		json.NewDecoder(resp.Body).Decode(&res)

		go openNewServerHandler(newPort, newWebsocketPort)

		ctx.JSON(consts.StatusOK, utils.H{"result": newWebsocketPort})
	})

	h.GET("/delete-host/:id", func(c context.Context, ctx *app.RequestContext) {
		hostId := ctx.Param("id")

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
		log.Fatal(err)
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

	h.Any("/*path", func(c context.Context, ctx *app.RequestContext) {

		body, _ := ctx.Body()

		requestChannel <- RequestInfo{
			Method:  string(ctx.Method()),
			Body:    string(body),
			Headers: ctx.GetRequest().Header.RawHeaders(),
			Path:    string(ctx.URI().RequestURI()),
		}

		responseData := <-responseChannel

		ctx.Response.SetStatusCode(responseData.StatusCode)

		for key, values := range responseData.Headers {
			for _, value := range values {
				ctx.Response.Header.Add(key, value)
			}
		}
		ctx.Response.SetBodyRaw([]byte(responseData.Body))
	})

	go openWebSocketServer(requestChannel, responseChannel, websocketPort)

	h.Spin()
}

func openWebSocketServer(requestChannel <-chan RequestInfo, responseChannel chan<- ResponseInfo, port string) {
	h := server.Default(
		server.WithHostPorts("0.0.0.0:" + port),
	)

	// TODO: handle no client element connected when receiving requests

	h.GET("/", func(c context.Context, ctx *app.RequestContext) {
		err := upgrader.Upgrade(ctx, func(conn *websocket.Conn) {
			for {
				request := <-requestChannel

				conn.WriteMessage(websocket.TextMessage, []byte(request.ToString()))

				_, responseMessage, err := conn.ReadMessage()
				if err != nil {
					log.Println("read:", err)
					break
				}

				response := ResponseInfo{}
				json.Unmarshal(responseMessage, &response)

				responseChannel <- response
			}
		})
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
	})

	h.Spin()
}
