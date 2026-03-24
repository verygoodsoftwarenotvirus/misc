package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/bwmarrin/discordgo"
)

const w2gCreateURL = "https://w2g-api.w2g.tv/rooms/create.json"

type w2gRoom struct {
	StreamKey string `json:"streamkey"`
}

func createW2GRoom() (string, error) {
	resp, err := http.Post(w2gCreateURL, "application/json", strings.NewReader("{}"))
	if err != nil {
		return "", fmt.Errorf("creating room: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var room w2gRoom
	if err := json.NewDecoder(resp.Body).Decode(&room); err != nil {
		return "", fmt.Errorf("decoding response: %w", err)
	}

	if room.StreamKey == "" {
		return "", fmt.Errorf("empty streamkey in response")
	}

	return fmt.Sprintf("https://w2g.tv/?r=%s", room.StreamKey), nil
}

func postToDiscord(link string) error {
	token := os.Getenv("DISCORD_BOT_TOKEN")
	channelID := os.Getenv("DISCORD_CHANNEL_ID")
	if token == "" || channelID == "" {
		return fmt.Errorf("DISCORD_BOT_TOKEN and DISCORD_CHANNEL_ID must be set")
	}
	if !strings.HasPrefix(token, "Bot ") {
		token = "Bot " + token
	}

	dg, err := discordgo.New(token)
	if err != nil {
		return fmt.Errorf("creating Discord session: %w", err)
	}

	if _, err = dg.ChannelMessageSend(channelID, link); err != nil {
		return fmt.Errorf("sending link: %w", err)
	}

	return nil
}

func main() {
	invite, err := createW2GRoom()
	if err != nil {
		log.Fatal(err)
	}

	if os.Getenv("DISCORD_BOT_TOKEN") != "" && os.Getenv("DISCORD_CHANNEL_ID") != "" {
		if err = postToDiscord(invite); err != nil {
			log.Fatal(err)
		}
	}
}
