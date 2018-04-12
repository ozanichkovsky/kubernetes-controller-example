package slack

import (
	"github.com/nlopes/slack"
	"fmt"
	"sync"
	"strings"
)

type Slack struct {
	api          *slack.Client
	apiBot       *slack.Client
	rtm          *slack.RTM
	rtmBot       *slack.RTM
	channels     <-chan string
	messages     <-chan Message
	incoming     chan Message
	done         <-chan interface{}
	channelCache map[string]string
	userCache	 map[string]string
	mux          sync.RWMutex
}

func NewSlack(token, botToken string) (*Slack, error) {
	api := slack.New(token)
	if _, err := api.AuthTest(); err != nil {
		return nil, fmt.Errorf("could not authenticate with slack: %v", err)
	}

	rtm := api.NewRTM()
	go rtm.ManageConnection()

	apiBot := slack.New(botToken)
	if _, err := apiBot.AuthTest(); err != nil {
		return nil, fmt.Errorf("could not authenticate with slack: %v", err)
	}

	rtmBot := apiBot.NewRTM()
	go rtmBot.ManageConnection()

	channels, err := api.GetChannels(false)
	if err != nil {
		fmt.Errorf("couldn't get channels: %v", err)
	}
	channelCache := make(map[string]string)
	for _, v := range channels {
		channelCache[v.Name] = v.ID
	}

	users, err := api.GetUsers()
	if err != nil {
		fmt.Errorf("couldn't get users: %v", err)
	}
	userCache := make(map[string]string)
	for _, v := range users {
		userCache[v.Name] = v.ID
	}

	return &Slack{
		api:          api,
		apiBot:       apiBot,
		rtm:          rtm,
		rtmBot:       rtmBot,
		channelCache: channelCache,
		userCache:    userCache,
	}, nil
}

func (s *Slack) disconnect() {
	s.rtm.Disconnect()
}

func (s *Slack) SetDone(done <-chan interface{}) {
	s.done = done
}

func (s *Slack) SetChannelChan(channels <-chan string) {
	s.channels = channels
}

func (s *Slack) SetMessageChan(messages <-chan Message) {
	s.messages = messages
}

func (s *Slack) GetIncomingMessagesChan() <-chan Message {
	if s.incoming == nil {
		s.incoming = make(chan Message)
	}

	return s.incoming
}

func (s *Slack) ensureChannelJoined(channelName string) {
	s.mux.RLock()
	channel, ok := s.channelCache[channelName]
	s.mux.RUnlock()
	var newChannel *slack.Channel
	if !ok {
		var err error
		newChannel, err = s.rtm.CreateChannel(channelName)
		if err != nil {
			fmt.Printf("error creating channel: %v\n", err)
			return
		}
	}


	if newChannel != nil {
		s.mux.Lock()
		s.channelCache[channelName] = newChannel.ID
		s.mux.Unlock()
	}


	_, err := s.rtm.JoinChannel(channelName)
	if err != nil {
		fmt.Printf("error joining channel: %v\n", err)
	}

	s.mux.RLock()
	_, err = s.api.InviteUserToChannel(channel, s.userCache["kubernetes_scaler"])
	if err != nil {
		fmt.Printf("Error inviting kubernetes_scaler into channel %s: %v\n",channel, err)
	}
	s.mux.RUnlock()

	s.mux.RLock()
	_, err = s.api.InviteUserToChannel(channel, s.userCache["testbot"])
	if err != nil {
		fmt.Printf("Error inviting testbot into channel %s: %v\n",channel, err)
	}
	s.mux.RUnlock()
}

func (s *Slack) sendMessage(message Message) {
	var channel string
	if message.ChannelId == "" {
		s.mux.RLock()
		var ok bool
		channel, ok = s.channelCache[message.Channel]
		s.mux.RUnlock()
		if !ok {
			return
		}
	} else {
		channel = message.ChannelId
	}

	msg := slack.OutgoingMessage{
		Channel: channel,
		Text:    message.Message,
		Type:    "message",
	}

	s.rtmBot.SendMessage(&msg)
}

func (s *Slack) Proceed() {
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
	loop:
		for {
			select {
			case ch, ok := <-s.channels:
				//println("channel")
				if !ok {
					s.channels = nil
					continue
				}
				s.ensureChannelJoined(ch)
			case message, ok := <-s.messages:
				//println("message")
				if !ok {
					s.messages = nil
					continue
				}
				s.sendMessage(message)
			case _, ok := <-s.done:
				//println("done")
				if !ok {
					s.done = nil
					s.disconnect()
					break loop
				}
			case msg := <-s.rtmBot.IncomingEvents:
				//println("msg")
				switch ev := msg.Data.(type) {
				case *slack.ConnectedEvent:
					fmt.Println("Connection counter:", ev.ConnectionCount)

				case *slack.MessageEvent:
					fmt.Printf("Message: %v\n", ev)
					info := s.rtmBot.GetInfo()
					prefix := fmt.Sprintf("<@%s> ", info.User.ID)

					if ev.User != info.User.ID && strings.HasPrefix(ev.Text, prefix) {
						channelInfo, err := s.rtmBot.GetChannelInfo(ev.Channel)
						if err != nil {
							fmt.Printf("Got error: %v", err)
						}

						s.rtmBot.SendMessage(s.rtm.NewOutgoingMessage("Processing your message", ev.Channel))
						s.incoming <- Message{
							ChannelId: ev.Channel,
							Message: ev.Text,
							Channel: channelInfo.Name,
						}
					}

				case *slack.RTMError:
					fmt.Printf("Error: %s\n", ev.Error())

				case *slack.InvalidAuthEvent:
					fmt.Printf("Invalid credentials")

				case *slack.ConnectionErrorEvent:
					fmt.Printf("Invalid credentials %T\n", ev)
				case *slack.AckErrorEvent:
					fmt.Printf("Error: %s\n", ev.Error())
				default:
					fmt.Printf("Type %T: \n", ev)
				}
			}
		}
	}()
	wg.Wait()
}

type Message struct {
	ChannelId string
	Channel   string
	Message   string
}
