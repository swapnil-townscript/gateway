package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"gateway/config"
	"gateway/logreport"
	"gateway/model"
	re "gateway/model/remote_endpoint"
	"gateway/queue"
	"gateway/queue/mangos"
	apsql "gateway/sql"

	"github.com/AnyPresence/surgemq/auth"
	"github.com/AnyPresence/surgemq/service"
	"github.com/AnyPresence/surgemq/sessions"
	"github.com/surgemq/message"
)

type MQTTPusher struct {
	push chan<- []byte
}

type MQTT struct {
	DB     *apsql.DB
	Server *service.Server
	Broker *mangos.Broker
}

type Context struct {
	RemoteEndpoint       *model.RemoteEndpoint
	PushPlatformCodename string
	ConnectTimeout       int
	AckTimeout           int
	TimeoutRetries       int
	DB                   *apsql.DB
}

func (c *Context) String() string {
	return fmt.Sprintf("%v,%v", c.RemoteEndpoint.ID, c.PushPlatformCodename)
}

func (c *Context) GetConnectTimeout() int {
	return c.ConnectTimeout
}

func (c *Context) GetAckTimeout() int {
	return c.AckTimeout
}

func (c *Context) GetTimeoutRetries() int {
	return c.TimeoutRetries
}

func NewMQTTPusher(pool *PushPool, platform *re.PushPlatform) *MQTTPusher {
	push, _ := pool.push.Channels()
	return &MQTTPusher{
		push: push,
	}
}

func (p *MQTTPusher) Push(channel *model.PushChannel, device *model.PushDevice, data interface{}) error {
	message := &PushMessage{
		Channel: channel,
		Device:  device,
		Data:    data,
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	p.push <- payload
	return nil
}

func SetupMQTT(db *apsql.DB, conf config.Push) *MQTT {
	server := &service.Server{
		KeepAlive:        300,
		ConnectTimeout:   2,
		SessionsProvider: "gateway",
		Authenticator:    "gateway",
		TopicsProvider:   "mem",
	}

	mqtt := &MQTT{
		DB:     db,
		Server: server,
	}

	auth.Register("gateway", mqtt)
	sessions.Register("gateway", NewDBProvider)

	go func() {
		if err := server.ListenAndServe("tcp://:1883"); err != nil {
			logreport.Fatal(err)
		}
	}()

	if conf.EnableBroker {
		var err error
		mqtt.Broker, err = mangos.NewBroker(mangos.XPubXSub, mangos.TCP, conf.XPub(), conf.XSub())
		if err != nil {
			log.Fatal(err)
		}
	}

	go func() {
		receive, err := queue.Subscribe(
			conf.XPub(),
			mangos.Sub,
			mangos.SubTCP,
		)
		if err != nil {
			logreport.Fatal(err)
		}
		messages, errs := receive.Channels()
		defer func() {
			receive.Close()
		}()
		go func() {
			for err := range errs {
				logreport.Printf("[mqtt] %v", err)
			}
		}()
		for msg := range messages {
			push := &PushMessage{}
			err := json.Unmarshal(msg, push)
			if err != nil {
				log.Fatal(err)
			}
			endpoint, err := model.FindRemoteEndpointForAPIIDAndAccountID(db, push.Channel.RemoteEndpointID,
				push.Channel.APIID, push.Channel.AccountID)
			if err != nil {
				logreport.Printf("[mqtt] %v", err)
			}
			context := &Context{
				RemoteEndpoint:       endpoint,
				PushPlatformCodename: push.Device.Type,
			}
			pubmsg := message.NewPublishMessage()
			pubmsg.SetTopic([]byte(fmt.Sprintf("/%s", push.Channel.Name)))
			pubmsg.SetQoS(0)
			payload, err := json.Marshal(push.Data)
			if err != nil {
				log.Fatal(err)
			}
			pubmsg.SetPayload(payload)
			err = server.Publish(context, pubmsg, nil, push.Device.Token)
			if err != nil {
				logreport.Printf("[mqtt] %v", err)
			}
		}
	}()

	return mqtt
}

func (m *MQTT) Authenticate(id string, cred interface{}) (fmt.Stringer, error) {
	username := strings.Split(id, ",")
	if len(username) != 4 && len(username) != 5 {
		return nil, errors.New("user name should have format: '<emai>,<api name>,<remote endpoint codename>,<push platform codename>[,<environment name>]'")
	}
	environmentName := ""
	if len(username) == 5 {
		environmentName = username[4]
	}

	user, err := model.FindUserByEmail(m.DB, username[0])
	if err != nil {
		return nil, err
	}

	endpoints, err := model.FindRemoteEndpointForCodenameAndAPINameAndAccountID(m.DB, username[2], username[1], user.AccountID)
	if err != nil {
		return nil, err
	}
	if len(endpoints) != 1 {
		return nil, errors.New("invalid credentials")
	}

	parent, endpoint, codename := &re.Push{}, endpoints[0], username[3]
	err = json.Unmarshal(endpoint.Data, parent)
	if err != nil {
		return nil, err
	}
	push := &re.Push{}
	if environmentName != "" {
		for _, environment := range endpoint.EnvironmentData {
			if environment.Name == environmentName {
				err = json.Unmarshal(environment.Data, push)
				if err != nil {
					return nil, err
				}
				break
			}
		}
	}
	push.UpdateWith(parent)

	context := &Context{
		RemoteEndpoint:       endpoint,
		PushPlatformCodename: codename,
		DB:                   m.DB,
	}

	found := false
	for _, platform := range push.PushPlatforms {
		if platform.Codename == codename {
			found = true
			if platform.Password != "" {
				if cred.(string) != platform.Password {
					return nil, errors.New("invalid credentials")
				}
			}
			context.ConnectTimeout = platform.ConnectTimeout
			context.AckTimeout = platform.AckTimeout
			context.TimeoutRetries = platform.TimeoutRetries
			break
		}
	}

	if !found {
		return nil, fmt.Errorf("%v is not a valid platform", codename)
	}

	return context, nil
}

type dbSessionTopics struct {
	context *Context
	id      string
}

func (t *dbSessionTopics) InitTopics(msg *message.ConnectMessage) error {
	//TODO: check for clean session and delete topics if true
	return nil
}

func (t *dbSessionTopics) AddTopic(topic string, qos byte) error {
	topic = strings.TrimLeft(topic, "/")
	channel := &model.PushChannel{
		AccountID:        t.context.RemoteEndpoint.AccountID,
		APIID:            t.context.RemoteEndpoint.APIID,
		RemoteEndpointID: t.context.RemoteEndpoint.ID,
		Name:             topic,
	}
	_channel, err := channel.Find(t.context.DB)
	expires := time.Now().Unix() + 60*60*24*365
	err = t.context.DB.DoInTransaction(func(tx *apsql.Tx) error {
		if err != nil {
			channel.Expires = expires
			err := channel.Insert(tx)
			if err != nil {
				return err
			}
		} else {
			channel = _channel
			if channel.Expires < expires {
				channel.Expires = expires
				err := channel.Update(tx)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	device := &model.PushDevice{
		AccountID:        t.context.RemoteEndpoint.AccountID,
		PushChannelID:    channel.ID,
		Token:            t.id,
		Name:             t.id,
		RemoteEndpointID: t.context.RemoteEndpoint.ID,
	}
	dev, err := device.Find(t.context.DB)
	err = t.context.DB.DoInTransaction(func(tx *apsql.Tx) error {
		update := false
		if err != nil {
			device.Type = re.PushTypeMQTT
			device.Expires = expires
			err = device.Insert(tx)
			if err != nil {
				return err
			}
		} else {
			update = true
		}
		if update {
			dev.PushChannelID = channel.ID
			dev.Expires = expires
			err := dev.Update(tx)
			if err != nil {
				return err
			}
		}
		return nil
	})

	return err
}

func (t *dbSessionTopics) RemoveTopic(topic string) error {
	topic = strings.TrimLeft(topic, "/")
	channel := &model.PushChannel{
		AccountID:        t.context.RemoteEndpoint.AccountID,
		APIID:            t.context.RemoteEndpoint.APIID,
		RemoteEndpointID: t.context.RemoteEndpoint.ID,
		Name:             topic,
	}
	channel, err := channel.Find(t.context.DB)
	if err != nil {
		return err
	}

	device := &model.PushDevice{
		AccountID:        t.context.RemoteEndpoint.AccountID,
		PushChannelID:    channel.ID,
		Name:             t.id,
		Token:            t.id,
		RemoteEndpointID: t.context.RemoteEndpoint.ID,
	}
	dev, err := device.Find(t.context.DB)
	if err != nil {
		return err
	}
	err = t.context.DB.DoInTransaction(func(tx *apsql.Tx) error {
		return dev.DeleteFromChannel(tx)
	})

	return err
}

func (t *dbSessionTopics) Topics() (topics []string, qoss []byte, err error) {
	channel := &model.PushChannel{
		AccountID:        t.context.RemoteEndpoint.AccountID,
		APIID:            t.context.RemoteEndpoint.APIID,
		RemoteEndpointID: t.context.RemoteEndpoint.ID,
	}
	channels, err := channel.AllForDeviceToken(t.context.DB, t.id)
	if err != nil {
		return nil, nil, err
	}

	for _, channel := range channels {
		topics = append(topics, fmt.Sprintf("/%v", channel.Name))
	}

	return
}

type dbProvider struct {
	context *Context
}

func NewDBProvider(context fmt.Stringer) sessions.SessionsProvider {
	return &dbProvider{
		context: context.(*Context),
	}
}

func (t *dbProvider) New(id string) (*sessions.Session, error) {
	session := &sessions.Session{Id: id, SessionTopics: &dbSessionTopics{id: id, context: t.context}}
	return session, nil
}

func (t *dbProvider) Get(id string) (*sessions.Session, error) {
	session := &sessions.Session{Id: id, SessionTopics: &dbSessionTopics{id: id, context: t.context}}
	return session, nil
}

func (t *dbProvider) Del(id string) {

}

func (t *dbProvider) Save(id string) error {
	return nil
}

func (t *dbProvider) Count() int {
	return 0
}

func (t *dbProvider) Close() error {
	return nil
}
