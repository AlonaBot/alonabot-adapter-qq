package qq_test

import (
	"fmt"
	qq "github.com/AlonaBot/alonabot-adapter-qq"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// TestQQAdapter 经生成资源、真实 Core/SDK 与本地 QQ 服务验证两种接入和按钮闭环。
func TestQQAdapter(t *testing.T) {
	// 显式依赖被测包，使测试缓存跟随 Adapter 源码变化。
	if got := qq.New().Descriptor().AdapterID; got != qq.AdapterID {
		t.Fatalf("adapter id: %q", got)
	}
	cli, err := exec.LookPath("alonabot")
	if err != nil {
		t.Fatal("alonabot CLI is required; see docs/development.md")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	files := map[string]string{
		"go.mod":            fmt.Sprintf("module qqintegration\n\ngo 1.27.3\n\nrequire github.com/AlonaBot/alonabot-adapter-qq v0.0.0\n\nreplace github.com/AlonaBot/alonabot-adapter-qq => %s\n", filepath.ToSlash(root)),
		"main.go":           integrationProgram,
		"probe/probe.go":    integrationPlugin,
		"probe/plugin.yaml": "id: alonabot-plugin-qq-integration\nname: QQ integration\nversion: 0.1.0\nauthor: AlonaBot\ndescription: Integration probe\nrole: plugin\nsource: github.com/AlonaBot/alonabot-adapter-qq\n",
	}
	for name, content := range files {
		filename := filepath.Join(directory, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.CommandContext(t.Context(), "go", "mod", "tidy")
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("resolve integration app: %v\n%s", err, output)
	}
	command = exec.CommandContext(t.Context(), cli, "build", directory, "--out", filepath.Join(directory, "bin"))
	// 带 race 的入口测试也让被构建的真实 Core 应用启用 race。
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "-race" && setting.Value == "true" {
				command.Env = append(os.Environ(), "GOFLAGS=-race")
			}
		}
	}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build integration app: %v\n%s", err, output)
	}
	binaries, err := os.ReadDir(filepath.Join(directory, "bin"))
	if err != nil || len(binaries) != 1 {
		t.Fatalf("integration executable: %v, %v", binaries, err)
	}
	command = exec.CommandContext(t.Context(), filepath.Join(directory, "bin", binaries[0].Name()))
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("QQ integration: %v\n%s", err, output)
	} else {
		t.Log(string(output))
	}
}

const integrationPlugin = `// Package probe 登记集成验收使用的消息与按钮处理函数。
package probe
import (
 "bytes"
 "github.com/AlonaBot/AlonaBot/sdk/media"
 "github.com/AlonaBot/AlonaBot/sdk"
 "github.com/AlonaBot/AlonaBot/sdk/button"
 "github.com/AlonaBot/AlonaBot/sdk/message"
)
// Failures 将 SDK Handler 的失败送回集成验收主循环。
var Failures=make(chan error,8)
// Value 保存按钮回调参数。
type Value struct { Text string ` + "`json:\"text\"`" + ` }
// New 创建真实 SDK 插件，回复 Markdown 与一个带类型值的按钮。
func New() *sdk.Plugin {
 p:=sdk.NewPlugin()
 action:=button.Define[Value]("choose")
 p.On(sdk.MessageCreated).Handle(func(c *sdk.Context) (handlerErr error) {
  defer func(){if handlerErr!=nil {Failures<-handlerErr}}()
  if c.Text()=="inline" {_,err:=c.Send(message.Image(message.MediaSource{InlineBase64:"UVEtbWVkaWE="}));return err}
  if c.Text()=="upload" {ref,err:=media.Upload(c,bytes.NewReader([]byte("QQ-media")),"image/png");if err!=nil{return err};_,err=c.Send(message.Image(ref.Source()));return err}
  for _,segment:=range c.Event().Message.Chain {
   switch segment.(type) {case message.ImageSegment,message.VideoSegment,message.VoiceSegment,message.FileSegment:
    _,err:=c.Send(message.Chain{segment});return err
   }
  }
  _,err:=c.Send(message.Chain{message.Markdown("choose"),message.Buttons([]message.Button{action.Button("确认",Value{Text:"selected"}),message.URLButton("文档","https://example.com")})})
  return err
 })
 button.On(p,action).Handle(func(c *sdk.Context,v Value) (handlerErr error) {
  defer func(){if handlerErr!=nil {Failures<-handlerErr}}()
  _,err:=c.Send(message.Text(v.Text+":"+c.Event().Actor.ExternalUserID))
  return err
 })
 return p
}
`

const integrationProgram = `package main
import (
 "bytes"
 "context"
 "crypto/ed25519"
 "encoding/hex"
 "encoding/json"
 "fmt"
 "io"
 "net"
 "net/http"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "sync"
 "time"
 "github.com/AlonaBot/AlonaBot"
 qq "github.com/AlonaBot/alonabot-adapter-qq"
 "github.com/coder/websocket"
 "qqintegration/probe"
)
type transport struct { target string; core string; base http.RoundTripper }
func (t transport) RoundTrip(r *http.Request) (*http.Response,error) {
 clone:=r.Clone(r.Context()); address:=*r.URL;clone.URL=&address
 if address.Host=="api.sgroup.qq.com" || address.Host=="bots.qq.com" || address.Host=="media.qq.example" {
  clone.URL.Scheme="http";clone.URL.Host=strings.TrimPrefix(t.target,"http://")
 }
 if address.Host=="core.qq.example" {clone.URL.Scheme="http";clone.URL.Host=t.core}
 return t.base.RoundTrip(clone)
}
func check(err error) {if err!=nil {panic(err)}}
func expect(ok bool,format string,args ...any){if !ok {panic(fmt.Sprintf(format,args...))}}
func jsonResponse(w http.ResponseWriter,value any){check(json.NewEncoder(w).Encode(value))}
func main(){
 ctx,cancel:=context.WithTimeout(context.Background(),45*time.Second);defer cancel()
 for _,mode:=range []string{"webhook","websocket"} {run(ctx,mode)}
 fmt.Println("QQ integration passed: real Core, both transports, four scenes, callbacks and deduplication")
}
func run(ctx context.Context,mode string){
 var mu sync.Mutex
 var endpoint string
 var captured []map[string]any
 sends:=make(chan map[string]any,32)
 gateway:=make(chan *websocket.Conn,1)
 handler:=http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){
  switch {
  case r.URL.Path=="/app/getAppAccessToken":jsonResponse(w,map[string]any{"access_token":"token","expires_in":"3600"})
  case r.URL.Path=="/users/@me":
   expect(r.Header.Get("Authorization")=="QQBot token","missing QQ authentication")
   jsonResponse(w,map[string]string{"id":"bot","username":"QQ bot"})
  case r.URL.Path=="/gateway":jsonResponse(w,map[string]string{"url":"ws"+strings.TrimPrefix(endpoint,"http")+"/ws"})
  case r.URL.Path=="/ws":
   c,err:=websocket.Accept(w,r,nil);check(err)
   encoded,_:=json.Marshal(map[string]any{"op":10,"d":map[string]int{"heartbeat_interval":1000}})
   check(c.Write(ctx,websocket.MessageText,encoded))
   _,raw,err:=c.Read(ctx);check(err)
   var auth map[string]any;check(json.Unmarshal(raw,&auth));expect(auth["op"]==float64(2),"expected identify: %s",raw)
   writeWS(ctx,c,map[string]any{"op":0,"s":1,"t":"READY","d":map[string]string{"session_id":"session"}})
   gateway<-c
   for {_,raw,err:=c.Read(ctx);if err!=nil{return};var p map[string]any;check(json.Unmarshal(raw,&p));if p["op"]==float64(1){writeWS(ctx,c,map[string]any{"op":11,"d":nil})}}
  case strings.HasPrefix(r.URL.Path,"/interactions/"):
   expect(r.Method=="PUT","wrong interaction method")
   jsonResponse(w,map[string]any{})
  case r.URL.Path=="/attachment":
   w.Header().Set("Content-Length","8");_,err:=io.WriteString(w,"QQ-media");check(err)
  case strings.HasSuffix(r.URL.Path,"/files"):
   var upload map[string]any;check(json.NewDecoder(r.Body).Decode(&upload))
   if data,ok:=upload["file_data"].(string);ok {expect(data=="UVEtbWVkaWE=" && upload["file_name"]!="","invalid direct upload");jsonResponse(w,map[string]string{"file_info":"direct-file-info"});return}
   mediaURL,ok:=upload["url"].(string);expect(ok && mediaURL!="","media upload has no URL")
   response,err:=http.Get(mediaURL);check(err);data,err:=io.ReadAll(response.Body);check(err);check(response.Body.Close())
   expect(response.StatusCode==200 && string(data)=="QQ-media","Core media relay failed: %d %q",response.StatusCode,data)
   jsonResponse(w,map[string]string{"file_info":"media-file-info"})
  case strings.HasSuffix(r.URL.Path,"/messages"):
   var body map[string]any;check(json.NewDecoder(r.Body).Decode(&body))
   mu.Lock();captured=append(captured,body);id:=fmt.Sprint(len(captured));mu.Unlock()
   jsonResponse(w,map[string]string{"id":id})
   body["_test_message_id"]=id
   sends<-body
  default:panic("unexpected QQ request: "+r.URL.Path)
  }
 })
 platform:=httptest.NewServer(handler);defer platform.Close();endpoint=platform.URL
 old:=http.DefaultTransport;defer func(){http.DefaultTransport=old}()
 listener,err:=net.Listen("tcp","127.0.0.1:0");check(err);address:=listener.Addr().String();check(listener.Close());http.DefaultTransport=transport{endpoint,address,old}
 directory,err:=os.MkdirTemp("","qq-real-core-");check(err);defer os.RemoveAll(directory)
 config:=map[string]any{"listen":address,"base_url":"https://core.qq.example","adapters":map[string]any{"qq":map[string]any{"instances":map[string]any{"test":map[string]any{"app_id":"123","app_secret":"secret","mode":mode}}}}}
 encoded,err:=json.Marshal(config);check(err);check(os.WriteFile(filepath.Join(directory,"core_config.json"),encoded,0600))
 lifetime,stop:=context.WithCancel(ctx)
 a:=qq.New()
 core,err:=alonabot.Start(lifetime,alonabot.DataDir(directory),alonabot.Adapter(a),alonabot.Plugin(probe.New()));check(err)
 defer func(){stop();check(core.Wait())}()
 var connection *websocket.Conn
 if mode=="websocket" {select {case connection=<-gateway:case <-ctx.Done():panic(ctx.Err())}}
 // HTTP 测试通过真实 Core 代理进入 SDK 托管路由。
 webhookURL:="http://"+address+"/plugins/alonabot-adapter-qq/webhook/test"
 sequence:=1
 deliver:=func(event map[string]any){
  sequence++;event["s"]=sequence
  if mode=="websocket" {writeWS(ctx,connection,event);return}
  raw,err:=json.Marshal(event);check(err)
  request,err:=http.NewRequestWithContext(ctx,"POST",webhookURL,bytes.NewReader(raw));check(err)
  request.Host="core.qq.example"
  timestamp:="123456"
  seed:=strings.Repeat("secret",6)[:32];key:=ed25519.NewKeyFromSeed([]byte(seed))
  request.Header.Set("X-Signature-Timestamp",timestamp)
  request.Header.Set("X-Signature-Ed25519",hex.EncodeToString(ed25519.Sign(key,append([]byte(timestamp),raw...))))
  response,err:=http.DefaultClient.Do(request);check(err);body,err:=io.ReadAll(response.Body);check(err);check(response.Body.Close())
  expect(response.StatusCode==200,"webhook HTTP %d: %s",response.StatusCode,body)
 }
 waitSend:=func()map[string]any {select {case body:=<-sends:return body;case err:=<-probe.Failures:panic(err);case <-ctx.Done():panic(ctx.Err())}}
 for index,scene:=range []string{"group","c2c","guild","dm"} {
  kind:=map[string]string{"group":"GROUP_AT_MESSAGE_CREATE","c2c":"C2C_MESSAGE_CREATE","guild":"AT_MESSAGE_CREATE","dm":"DIRECT_MESSAGE_CREATE"}[scene]
  incoming:=map[string]any{"id":fmt.Sprint("incoming-",index),"content":"hello","timestamp":time.Now().UTC().Format(time.RFC3339Nano),"author":map[string]string{"id":"user","member_openid":"user","user_openid":"user"},"group_openid":"group","channel_id":"channel","guild_id":"dm"}
  event:=map[string]any{"op":0,"id":"message-"+scene,"t":kind,"d":incoming}
  deliver(event)
  sent:=waitSend()
  expect(sent["markdown"]!=nil && sent["keyboard"]!=nil,"missing Markdown or keyboard: %v",sent)
  expect(sent["msg_id"]==incoming["id"],"missing passive reply id: %v",sent)
  rows:=sent["keyboard"].(map[string]any)["content"].(map[string]any)["rows"].([]any)
  buttons:=rows[0].(map[string]any)["buttons"].([]any)
  token:=buttons[0].(map[string]any)["action"].(map[string]any)["data"].(string)
  expect(len(token)==43,"callback must carry Core token: %q",token)
  expect(buttons[1].(map[string]any)["action"].(map[string]any)["type"]==float64(0),"URL button type is wrong")

  click:=map[string]any{"id":"click-"+scene,"application_id":"123","type":11,"scene":map[string]string{"group":"group","c2c":"c2c","guild":"guild","dm":"guild"}[scene],"timestamp":time.Now().UTC().Format(time.RFC3339Nano),"group_openid":"group","group_member_openid":"user","user_openid":"user","channel_id":"channel","data":map[string]any{"resolved":map[string]string{"button_data":token,"user_id":"user","message_id":sent["_test_message_id"].(string)}}}
  deliver(map[string]any{"op":0,"id":"INTERACTION_CREATE:click-"+scene,"t":"INTERACTION_CREATE","d":click})
  reply:=waitSend()
  expect(reply["content"]=="selected:user","button did not reach typed plugin handler: %v",reply)
  expect(reply["event_id"]=="INTERACTION_CREATE:click-"+scene,"button reply lost gateway event id: %v",reply)
  // 重放相同平台事件，下一条消息的回执必须先于任何重复业务回复。
  deliver(event)
 }
 for _,scene:=range []string{"group","c2c"} {
  for _,mime:=range []string{"image/jpeg","video/mp4","voice","file"} {
   kind:="GROUP_MESSAGE_CREATE";if scene=="c2c" {kind="C2C_MESSAGE_CREATE"}
   eventID:="media-"+scene+"-"+mime
   incoming:=map[string]any{"id":eventID,"content":"","timestamp":time.Now().UTC().Format(time.RFC3339Nano),"author":map[string]string{"member_openid":"user","user_openid":"user"},"group_openid":"group","attachments":[]any{map[string]any{"url":"https://media.qq.example/attachment","filename":"sample","content_type":mime,"size":8}}}
   deliver(map[string]any{"op":0,"id":eventID,"t":kind,"d":incoming})
   sent:=waitSend();expect(sent["msg_type"]==float64(7) && sent["media"]!=nil,"media send failed: %v",sent)
  }
 }
 deliver(map[string]any{"op":0,"id":"uploaded-media","t":"GROUP_MESSAGE_CREATE","d":map[string]any{"id":"uploaded","content":"upload","timestamp":time.Now().UTC().Format(time.RFC3339Nano),"author":map[string]string{"member_openid":"user"},"group_openid":"group"}})
 uploaded:=waitSend();expect(uploaded["msg_type"]==float64(7),"uploaded Core media send failed: %v",uploaded)
 deliver(map[string]any{"op":0,"id":"inline-media","t":"C2C_MESSAGE_CREATE","d":map[string]any{"id":"inline","content":"inline","timestamp":time.Now().UTC().Format(time.RFC3339Nano),"author":map[string]string{"user_openid":"user"}}})
 direct:=waitSend();expect(direct["msg_type"]==float64(7),"inline media send failed: %v",direct)
 select {case duplicate:=<-sends:panic(fmt.Sprintf("duplicate delivery caused a send: %v",duplicate));case <-time.After(100*time.Millisecond):}
}
func writeWS(ctx context.Context,c *websocket.Conn,p any){raw,err:=json.Marshal(p);check(err);check(c.Write(ctx,websocket.MessageText,raw))}
`
