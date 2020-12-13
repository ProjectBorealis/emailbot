package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

	"github.com/bwmarrin/discordgo"
	"github.com/mailgun/mailgun-go/v3"
)

type Config struct {
	DiscordName                string `json:"discord_name"`
	DiscordToken               string `json:"discord_token"`
	DiscordServerID            string `json:"discord_server_id"`
	DiscordSetupChannelID      string `json:"discord_setup_channel_id"`
	MailgunDomain              string `json:"mailgun_domain"`
	MailgunPrivateKey          string `json:"mailgun_private_key"`
	MailgunRouteIdentityPrefix string `json:"mailgun_route_identity_prefix"`
}

const (
	DiscordEmailUsage     = "`!email <name> <email address>` - for example: `!email first.last email@example.org` would forward emails sent to `first.last@%s` to the mailbox `email@example.org`."
	DiscordConfiguredInfo = "Your forwarding address %s@%s has been configured. This allows you to receive email to your personal mailbox. To send email from this address, you may need to configure your mail client with these SMTP details:\n```server: mxb.mailgun.org\nusername: %s\npassword: %s```"
)

var EmailValidator = regexp.MustCompile("^[a-zA-Z0-9.!#$%&'*+/=?^_`{|}~-]+@[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?(?:\\.[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?)*$")

func main() {
	var config Config
	data, err := ioutil.ReadFile("config.json")
	if err != nil {
		panic(err)
	}
	if err = json.Unmarshal(data, &config); err != nil {
		panic(err)
	}

	mg := mailgun.NewMailgun(config.MailgunDomain, config.MailgunPrivateKey)

	f, err := NewEmailForwarder(mg, config.MailgunRouteIdentityPrefix)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	dg, err := discordgo.New(config.DiscordToken)
	if err != nil {
		panic(err)
	}

	dg.GuildMemberNickname(config.DiscordServerID, "@me", config.DiscordName)

	// Handle email forwarder requests
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}

		// !email command only
		fields := strings.Fields(m.Content)
		if len(fields) <= 0 || fields[0] != "!email" {
			return
		}

		// If not a direct message, ensure its in the setup channel
		channel, err := s.Channel(m.ChannelID)
		if err != nil {
			return
		}
		if channel.Type != discordgo.ChannelTypeDM && channel.ID != config.DiscordSetupChannelID {
			return
		}

		// Check the user is a member of the discord server
		member, err := s.GuildMember(config.DiscordServerID, m.Author.ID)
		if err != nil || member == nil {
			return
		}

		// validate
		if len(fields) < 3 || !EmailValidator.MatchString(fields[2]) {
			s.ChannelMessageSend(config.DiscordSetupChannelID, fmt.Sprintf(DiscordEmailUsage, config.MailgunDomain))
			return
		}

		// forward
		fields[1] = strings.TrimSuffix(fields[1], config.MailgunDomain)
		user, pass, err := f.Forward(fields[1], fields[2], m.Author.ID)
		if err != nil {
			s.ChannelMessageSend(config.DiscordSetupChannelID, fmt.Sprintf("Error configuring `%s:%s`: %v", fields[1], config.MailgunDomain, err))
			return
		}

		dm, err := s.UserChannelCreate(m.Author.ID)
		if err != nil {
			return
		}

		// send user smtp details
		s.ChannelMessageSend(dm.ID, fmt.Sprintf(DiscordConfiguredInfo, fields[1], config.MailgunDomain, user, pass))

		// audit log
		s.ChannelMessageSend(config.DiscordSetupChannelID, fmt.Sprintf("Configured `%s@%s` for <@%s>", fields[1], config.MailgunDomain, m.Author.ID))
	})

	err = dg.Open()
	if err != nil {
		panic(err)
	}

	fmt.Println("Bot is now running. Press CTRL-C to exit.")
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc

	dg.Close()
}
